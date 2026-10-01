package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestStartupDoesNotWaitForSourceModelAndIndependentClassifierRemainsAvailable(t *testing.T) {
	var modelCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		modelCalls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	catalog := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "topic", Label: "Topic", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "reference", Label: "Reference", Active: true}}}
	spec, err := classify.CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	workerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/enrichment/source-lease-capability":
			_, _ = fmt.Fprint(w, `{"protocol":1,"lease_ms":900000,"paid_stage_admission":true,"provider_result_guard":true,"completion_replay":true,"provider_attempt_ledger":true,"refresh_source_checkpoint":true,"source_component_gate":true,"source_stage_pause":true}`)
		case "/api/v2/taxonomy":
			_ = json.NewEncoder(w).Encode(catalog)
		case "/api/v2/question-specs":
			_, _ = fmt.Fprint(w, `{}`)
		case "/api/enrichment/classifications/target":
			w.Header().Set("X-Cairn-Classification-Budget", "1")
			_ = json.NewEncoder(w).Encode(map[string]any{"target": map[string]any{"generation": 1, "protocol": "v2", "spec_id": spec.SpecID, "spec_hash": spec.SemanticHash, "taxonomy_version": catalog.Version, "policy_version": classify.PolicyVersion, "requested_model": "jev-1.13.0"}, "supported": true})
		case "/api/enrichment/classifications/claim":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected startup API %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer workerAPI.Close()
	cfg := config.Config{CairnBaseURL: workerAPI.URL, CairnToken: "fixture", GrokBaseURL: provider.URL, GrokAPIKey: "fixture", GrokModel: "fixture", TypesafeBaseURL: provider.URL, TypesafeAPIKey: "fixture", TypesafeModel: "jev-1.13.0", WorkerRequestTimeout: time.Second, GrokFetchTimeout: time.Second, GrokReadingTimeout: time.Second, TypesafeRequestTimeout: time.Second, MaxConcurrency: 1, ShutdownTimeout: time.Second}
	tracker := health.NewTracker()
	worker, _, err := newProcessor(context.Background(), cfg, tracker, discardLogger(), true)
	if err != nil || modelCalls.Load() != 0 {
		t.Fatalf("Reader startup depended on source model: calls=%d err=%v", modelCalls.Load(), err)
	}
	if done, _, err := worker.RunClassifications(context.Background(), 1); err != nil || done != 0 || modelCalls.Load() != 0 {
		t.Fatalf("source pending blocked independent classification: err=%v", err)
	}
	if tracker.Snapshot().DegradedComponents["source"] == "" {
		t.Fatal("pending source stage invisible")
	}
}
