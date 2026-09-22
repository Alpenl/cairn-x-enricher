package localintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerDecisionReferences(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language model engineering", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Description: "Procedure", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try later", Active: true}}}
	spec := mustSpec(t, catalog)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		model := "jev-1.13.0"
		if calls.Add(1) == 3 {
			model = "jev-1.14.0"
		}
		answers := map[string]any{}
		for _, q := range spec.Questions {
			if q.Kind == classify.QuestionNoul {
				answers[q.ID] = map[string]any{"type": "noul", "noul": .9}
				continue
			}
			options := q.AnswerOptions()
			distribution := map[string]float64{}
			for _, option := range options {
				distribution[option] = 0
			}
			distribution[options[0]] = 1
			answers[q.ID] = map[string]any{"type": "choice", "choice": options[0], "probabilities": distribution, "confidence": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "answers": answers, "usage": map[string]int{"input_tokens": 30, "output_tokens": 3}})
	}))
	defer provider.Close()
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	client, err := classify.NewClient(provider.URL, "fixture", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, client.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, client)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "Language model engineering.", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	p := processor.NewStaged(queue, nil, client, catalog.Version, "jev-latest", nil, 1)
	run := func(retry bool) cairn.StoredRun {
		t.Helper()
		if retry {
			postJSON(ctx, t, fmt.Sprintf("%s/api/enrichment/classifications/%d/retry", base, id), token, map[string]any{})
		}
		done, failed, err := p.RunClassifications(ctx, 1)
		if err != nil || done != 1 || failed != 0 {
			t.Fatalf("classification %d/%d: %v", done, failed, err)
		}
		result, err := queue.GetLatestRun(ctx, id)
		if err != nil || result == nil {
			t.Fatalf("stored run: %v", err)
		}
		return *result
	}
	first := run(false)
	initial, err := queue.GetLatestDecision(ctx, id)
	if err != nil || initial == nil || !initial.RunReferencesComplete || !reflect.DeepEqual(initial.RunIDs, []int64{first.ID}) {
		t.Fatalf("normal completion references: %+v %v", initial, err)
	}
	second := run(true)
	raw, err := second.DecodeJudgments(client.Spec())
	if err != nil {
		t.Fatal(err)
	}
	policy := client.Policy()
	policy.Version += "-replay"
	proposed, err := classify.Decide(raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"operation_key": "multi-decision", "run_ids": []int64{second.ID, first.ID}, "policy_version": policy.Version, "policy": policy,
		"spec_id": first.SpecID, "spec_hash": first.SpecHash, "resolved_model": first.ResolvedModel, "content_revision": first.ContentRevision, "expected_revision": 0,
		"automatic": classify.AutomaticFromProposals(proposed)}
	if err := queue.SubmitDecision(ctx, id, body); err != nil {
		t.Fatal(err)
	}
	stored, err := queue.GetLatestDecision(ctx, id)
	if err != nil || stored == nil || !stored.RunReferencesComplete || !reflect.DeepEqual(stored.RunIDs, []int64{first.ID, second.ID}) || stored.ExpectedPersonalRevision == nil || *stored.ExpectedPersonalRevision != 0 {
		t.Fatalf("multi-run read: %+v %v", stored, err)
	}
	if calls.Load() != 2 {
		t.Fatal("policy replay invoked model")
	}
	third := run(true)
	if third.RequestedModel != first.RequestedModel || third.ResolvedModel == first.ResolvedModel {
		t.Fatal("fixture did not exercise actual alias drift")
	}
	delete(body, "resolved_model")
	body["operation_key"] = "mixed-model"
	body["run_ids"] = []int64{first.ID, third.ID}
	if err := queue.SubmitDecision(ctx, id, body); err == nil {
		t.Fatal("mixed resolved models accepted")
	}
	body["resolved_model"] = first.ResolvedModel
	body["operation_key"] = "lost-decision-response"
	body["run_ids"] = []int64{first.ID, second.ID}
	lost := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != 200 {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, errors.New("fixture: response lost after committed decision")
	})})
	if err := lost.SubmitDecision(ctx, id, body); err == nil || !strings.Contains(err.Error(), "response lost") {
		t.Fatalf("lost response not exercised: %v", err)
	}
	committed, err := queue.GetLatestDecision(ctx, id)
	if err != nil || committed == nil {
		t.Fatal(err)
	}
	rev := int64(0)
	if _, err := queue.ApplyV2Override(ctx, id, cairn.V2Override{OperationKey: "human-reject", Field: "topics", Action: "reject", Term: "llm", ExpectedRevision: &rev}); err != nil {
		t.Fatal(err)
	}
	source.ContextText = "New objective material after the decision."
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now().Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitDecision(ctx, id, body); err != nil {
		t.Fatalf("successful operation could not be confirmed after state advanced: %v", err)
	}
	after, err := queue.GetLatestDecision(ctx, id)
	if err != nil || after == nil || after.ID != committed.ID || !reflect.DeepEqual(after.RunIDs, committed.RunIDs) {
		t.Fatalf("retry rewrote decision: %+v %v", after, err)
	}
	body["operation_key"] = "new-stale-decision"
	if err := queue.SubmitDecision(ctx, id, body); err == nil {
		t.Fatal("new stale CAS accepted")
	}
	view, err := queue.GetV2Selection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Selection.Topics) != 0 || view.Revision != 1 {
		t.Fatalf("policy replay lost human rejection: %+v", view)
	}
	runs, err := queue.GetRuns(ctx, id)
	if err != nil || len(runs) != 3 || calls.Load() != 3 {
		t.Fatalf("pure decision changed inference count: runs=%d calls=%d err=%v", len(runs), calls.Load(), err)
	}
	t.Log("production multi-run references restored; alias drift rejected; committed response-loss recovery preserved human edit and made zero additional model calls; 3 local calls total")
}
