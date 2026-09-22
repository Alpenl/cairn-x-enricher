package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerEntitySnapshotIdentity(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1",
		Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language models", Active: true}},
		Forms:  []taxonomy.Term{{ID: "method", Label: "Method", Description: "Method", Active: true}},
		Uses:   []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try", Active: true}}}
	ordinary := providerContractServer(t, mustSpec(t, catalog))
	defer ordinary.Close()
	var entityCalls, classificationCalls atomic.Int64
	var analyzed []extension.Block
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var payload struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if _, ok := payload.Questions["entity_0"]; !ok {
			classificationCalls.Add(1)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			ordinary.Config.Handler.ServeHTTP(w, r)
			return
		}
		entityCalls.Add(1)
		var state struct {
			Material []extension.Block `json:"material"`
		}
		if err := json.Unmarshal(payload.State, &state); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		analyzed = state.Material
		answers := map[string]any{}
		for id := range payload.Questions {
			answers[id] = map[string]any{"type": "noul", "noul": 0.95}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned-local", "answers": answers, "usage": map[string]int{"input_tokens": 20, "output_tokens": 2}})
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	var leased cairn.ClassificationJob
	var submitted map[string]any
	client := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/entity-state") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			if err := json.Unmarshal(body, &submitted); err != nil {
				return nil, err
			}
		}
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && response.StatusCode == 200 && strings.HasSuffix(r.URL.Path, "/classifications/claim") {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			if err := json.Unmarshal(body, &leased); err != nil {
				return nil, err
			}
		}
		return response, err
	})}
	queue := cairn.NewClient(base, token, client)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "ordinary source text", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{"blocks": []map[string]string{
		{"id": "tweet-789", "role": "primary", "text": source.OriginalText},
		{"id": "article-456", "role": "external_article", "text": "ExternalEntity", "url": "https://allowed.example/article"},
		{"id": "quotation-321", "role": "quoted", "text": "QuotedEntity"},
	}, "retrieval": "manual", "fetched_at": "2026-09-22T00:00:00Z", "truncation": map[string]bool{"truncated": false}}
	if err := queue.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	p := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	p.SetExtensions(extension.NewService(extension.Flags{Entities: true}, extension.DefaultBudget(), classifier), nil, extension.FetchPolicy{})
	done, failed, err := p.RunClassifications(ctx, 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("processing: %d %d %v", done, failed, err)
	}
	if leased.ContentRevision == leased.InputRevision {
		t.Fatalf("fixture did not separate revisions: %+v", leased)
	}
	if submitted["content_revision"] != float64(leased.ContentRevision) || submitted["content_hash"] != leased.EvidenceHash || submitted["evidence_snapshot_id"] != float64(leased.EvidenceSnapshotID) {
		t.Fatalf("wrong identity: %+v", submitted)
	}
	if len(analyzed) != 3 || analyzed[0].ID != "tweet-789" || analyzed[1].ID != "article-456" || analyzed[1].Text != "ExternalEntity" || analyzed[1].Role != "external_article" || analyzed[2].ID != "quotation-321" {
		t.Fatalf("not archived blocks: %+v", analyzed)
	}
	if entityCalls.Load() != 1 || classificationCalls.Load() != 1 {
		t.Fatalf("calls: entities=%d classification=%d", entityCalls.Load(), classificationCalls.Load())
	}
	get := func() []string {
		raw, err := queue.GetEntities(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Entities []string `json:"entities"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result.Entities
	}
	if !slices.Equal(get(), []string{"ExternalEntity", "QuotedEntity"}) {
		t.Fatalf("entities not stored: %v", get())
	}
	for index, action := range []string{"reject", "reset", "set_empty", "reset"} {
		term := "ExternalEntity"
		if action == "set_empty" {
			term = ""
		}
		if err := queue.CorrectEntity(ctx, id, map[string]any{"operation_key": fmt.Sprintf("entity-human-%d-%s", index, action), "term": term, "action": action}); err != nil {
			t.Fatal(err)
		}
		want := action == "reset"
		if slices.Contains(get(), "ExternalEntity") != want {
			t.Fatalf("%s not reflected: %v", action, get())
		}
		if index == 3 && !slices.Equal(get(), []string{"ExternalEntity"}) {
			t.Fatalf("term reset revived an unrelated entity: %v", get())
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/enrichment/jobs?q=ExternalEntity", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Items []json.RawMessage `json:"items"`
		}
		decodeErr := json.NewDecoder(res.Body).Decode(&result)
		_ = res.Body.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if (len(result.Items) == 1) != want {
			t.Fatalf("%s search disagreement: %d", action, len(result.Items))
		}
	}
	// A newer snapshot makes this exact analyzed input obsolete. A fresh
	// operation using the old identity must fail, while its original receipt replays.
	snapshot["retrieval"] = "manual-revised"
	if err := queue.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEntityState(ctx, id, submitted); err != nil {
		t.Fatalf("lost-response receipt: %v", err)
	}
	submitted["operation_key"] = "stale-new-operation"
	if err := queue.SubmitEntityState(ctx, id, submitted); err == nil {
		t.Fatal("genuinely stale operation accepted")
	}
	if len(get()) != 0 {
		t.Fatalf("stale entities still automatic: %v", get())
	}
	t.Logf("actual processor: input_revision=%d content_revision=%d snapshot=%d; classification=1 entity=1; 3 archived blocks; reject/reset API+search agree; stale result refused", leased.InputRevision, leased.ContentRevision, leased.EvidenceSnapshotID)
}
