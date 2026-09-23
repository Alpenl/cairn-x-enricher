package localintegration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerObjectivePersonalBoundary(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}, {ID: "contra", Label: "Opposition", Active: true}}}
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Questions map[string]struct {
				Criteria map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if _, ok := request.Questions["use"].Criteria["contra"]; ok {
			t.Error("personal candidate leaked into actual provider request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "usage": map[string]int{"input_tokens": 30, "output_tokens": 3}, "answers": map[string]any{
			"topic_llm": map[string]any{"type": "noul", "noul": .9},
			"form":      map[string]any{"type": "choice", "choice": "method", "probabilities": map[string]float64{"method": .9, "none": .1}, "confidence": .8},
			"use":       map[string]any{"type": "choice", "choice": "try", "probabilities": map[string]float64{"try": .9, "none": .1}, "confidence": .8},
		}})
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "A method for evaluating language models.", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	job, err := queue.ClaimClassification(ctx, classifier.SpecID(), catalog.Version, "jev-latest")
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	result, err := classifier.Classify(ctx, job.Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"legacy", "automatic", "assessment"} {
		bad := result
		switch field {
		case "legacy":
			bad.Classification.Use = "contra"
		case "automatic":
			bad.Automatic.Use = "contra"
		case "assessment":
			bad.Automatic.Assessment = &classify.Assessment{Version: 1, Incomplete: []string{}, Decisions: []classify.FieldDecision{{Dimension: "use", Verdict: classify.VerdictAccepted, Value: "contra", Candidate: "contra", Probability: .99, Reason: "unsafe old policy"}}}
		}
		if err := queue.CompleteClassification(ctx, job, bad); err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract {
			t.Fatalf("%s completion not rejected as contract: %v", field, err)
		}
		runs, err := queue.GetRuns(ctx, id)
		if err != nil || len(runs) != 0 {
			t.Fatalf("invalid completion wrote runs: %d %v", len(runs), err)
		}
	}
	if err := queue.CompleteClassification(ctx, job, result); err != nil {
		t.Fatal(err)
	}
	stored, err := queue.GetLatestRun(ctx, id)
	if err != nil || stored == nil {
		t.Fatalf("stored run: %v", err)
	}
	previous, err := queue.GetLatestDecision(ctx, id)
	if err != nil || previous == nil {
		t.Fatalf("stored decision: %v", err)
	}
	revision := int64(0)
	if _, err := queue.ApplyV2Override(ctx, id, cairn.V2Override{OperationKey: "explicit-human-opposition", Field: "use", Action: "accept", Term: "contra", ExpectedRevision: &revision}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"operation_key": "objective-replay", "run_ids": []int64{stored.ID}, "policy_version": classify.PolicyVersion, "policy": classify.DefaultPolicy(), "spec_id": stored.SpecID, "spec_hash": stored.SpecHash, "resolved_model": stored.ResolvedModel, "content_revision": stored.ContentRevision, "expected_revision": 1, "automatic": result.Automatic}
	bad := result.Automatic
	bad.Use = "contra"
	body["automatic"] = bad
	if err := queue.SubmitDecision(ctx, id, body); err == nil {
		t.Fatal("unsafe legacy-style replay accepted")
	}
	unchanged, err := queue.GetLatestDecision(ctx, id)
	if err != nil || unchanged == nil || unchanged.ID != previous.ID {
		t.Fatalf("rejected replay changed decision: %v", err)
	}
	body["automatic"] = result.Automatic
	if err := queue.SubmitDecision(ctx, id, body); err != nil {
		t.Fatal(err)
	}
	raw, err := queue.GetV2Effective(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var effective struct {
		Effective struct {
			Use string `json:"use"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(raw, &effective); err != nil {
		t.Fatal(err)
	}
	if effective.Effective.Use != "contra" {
		t.Fatal("objective update lost explicit human opposition")
	}
	if calls.Load() != 1 {
		t.Fatalf("validation or replay reran model: %d", calls.Load())
	}
	t.Log("actual provider -> Go -> Worker/D1: 3 invalid v2 completions rejected without runs; valid completion and replay preserve explicit human opposition; unsafe replay rejected; 1 fixture model call total")
}
