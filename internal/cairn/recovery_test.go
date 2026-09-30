package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// TestCompleteClassificationRecoversALostResponse is the F10 regression: the
// commit landed but the response was lost. The exact same operation and result
// must be replayed; a bookmark-level completed status cannot confirm this commit.
func TestCompleteClassificationRecoversALostResponse(t *testing.T) {
	var mu sync.Mutex
	commits := 0
	var keys []string
	var payloads []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/enrichment/classifications/7/complete":
			mu.Lock()
			commits++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			keys = append(keys, body["operation_key"].(string))
			payload, _ := json.Marshal(body)
			payloads = append(payloads, string(payload))
			first := commits == 1
			mu.Unlock()
			if first {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"id":7,"status":"completed"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/enrichment/classifications/7":
			_, _ = w.Write([]byte(`{"id":7,"status":"completed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	job := &ClassificationJob{ID: 7, Revision: 2, InputRevision: 3, TargetGeneration: 1, SpecID: "classify-v1", LeaseToken: "lease"}
	if err := client.CompleteClassification(context.Background(), job, classify.Result{Model: "jev", PolicyVersion: "p"}); err != nil {
		t.Fatalf("a confirmed completion must recover: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if commits != 2 || keys[0] != keys[1] || payloads[0] != payloads[1] {
		t.Fatalf("must confirm identical operation without another inference: commits=%d keys=%v", commits, keys)
	}
}

func TestCompletedBookmarkDoesNotConfirmAnotherOperation(t *testing.T) {
	commits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":7,"status":"completed"}`))
			return
		}
		commits++
		if commits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"already_completed"}`))
	}))
	defer server.Close()
	job := &ClassificationJob{ID: 7, Revision: 2, LeaseToken: "different-lease"}
	err := NewClient(server.URL, "token", server.Client()).CompleteClassification(context.Background(), job, classify.Result{})
	if err == nil || commits != 2 {
		t.Fatalf("generic completed must not confirm this operation: err=%v commits=%d", err, commits)
	}
}

func TestCompletionRequiresMatchingAcknowledgment(t *testing.T) {
	for _, response := range []string{`{}`, `{"id":8,"status":"completed"}`, `{"id":7,"status":"processing"}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			err := NewClient(server.URL, "token", server.Client()).CompleteClassification(context.Background(), &ClassificationJob{ID: 7}, classify.Result{})
			if err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract {
				t.Fatalf("invalid acknowledgment must fail the contract: %v", err)
			}
		})
	}
}

// TestCompleteClassificationRetriesTheSameOperationKey is the F10 retry path:
// the first commit never landed, so the same key and payload are retried.
func TestCompleteClassificationRetriesTheSameOperationKey(t *testing.T) {
	var mu sync.Mutex
	commits := 0
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/enrichment/classifications/9/complete":
			mu.Lock()
			commits++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			keys = append(keys, body["operation_key"].(string))
			first := commits == 1
			mu.Unlock()
			if first {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{"id":9,"status":"completed"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/enrichment/classifications/9":
			_, _ = w.Write([]byte(`{"id":9,"status":"processing"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	job := &ClassificationJob{ID: 9, Revision: 2, InputRevision: 3, TargetGeneration: 1, SpecID: "classify-v1", LeaseToken: "lease"}
	if err := client.CompleteClassification(context.Background(), job, classify.Result{Model: "jev", PolicyVersion: "p"}); err != nil {
		t.Fatalf("bounded retry should succeed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if commits != 2 {
		t.Fatalf("commit attempts = %d, want 2", commits)
	}
	if keys[0] != keys[1] {
		t.Fatalf("retry used a different operation key: %v", keys)
	}
}

// TestCompleteClassificationDoesNotRetryAConflict ensures a different-payload
// conflict is surfaced, not retried into a loop.
func TestCompleteClassificationDoesNotRetryAConflict(t *testing.T) {
	commits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		commits++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"operation_conflict"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	job := &ClassificationJob{ID: 3, Revision: 1, InputRevision: 1, TargetGeneration: 1, SpecID: "classify-v1", LeaseToken: "lease"}
	err := client.CompleteClassification(context.Background(), job, classify.Result{Model: "jev", PolicyVersion: "p"})
	if err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract {
		t.Fatalf("conflict class = %s (%v), want contract", enrich.ClassOf(err), err)
	}
	if commits != 1 {
		t.Fatalf("a conflict must not be retried: %d commits", commits)
	}
}

// TestSelectionDecodesBothRealWorkerShapes is the F03 regression: the fallback
// response (why/curation_status) and the v2 response (definition_version,
// provenance, revised_at) both decode through the strict client.
func TestSelectionDecodesBothRealWorkerShapes(t *testing.T) {
	v2Shape := `{"id":1,"revision":4,
        "automatic":{"topics":["eval"],"content_functions":[],"carriers":[],"affordances":[],"form":"method","use":"try"},
		"selection":{"topics":["llm"],"content_functions":["method"],"carriers":["single_post"],"affordances":[],"form":"method","use":"try"},
		"taxonomy_version":"2026-09-20.1","definition_version":1,
		"provenance":{"source":"decision","overrides":true,"revision":4},
		"revised_at":"2026-09-21T00:00:00Z","v1_only":false,
		"v1_projection":{"topics":["llm"],"form":"method","use":"try"},
		"empty":{"topics":false,"form":false,"use":false},"why":"","curation_status":"kept"}`
	fallbackShape := `{"id":1,"selection":{"topics":["llm"],"content_functions":[],"carriers":[],"affordances":[],"form":"method","use":"try"},
		"revision":0,"v1_only":true,"why":"自己的原因","curation_status":"inbox",
		"taxonomy_version":"2026-09-20.1","definition_version":1,"provenance":{},"revised_at":"2026-09-20T00:00:00Z",
		"v1_projection":{"topics":["llm"],"form":"method","use":"try"},"empty":{}}`
	for name, payload := range map[string]string{"v2": v2Shape, "fallback": fallbackShape} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(payload))
			}))
			defer server.Close()
			view, err := NewClient(server.URL, "token", server.Client()).GetV2Selection(context.Background(), 1)
			if err != nil {
				t.Fatalf("real Worker shape rejected: %v", err)
			}
			if !view.Available || view.Selection.Topics[0] != "llm" {
				t.Fatalf("decoded view is wrong: %+v", view)
			}
			if name == "v2" && (view.Automatic == nil || view.Automatic.Topics[0] != "eval") {
				t.Fatalf("independent automatic baseline lost: %+v", view)
			}
			if name == "fallback" && view.Automatic != nil {
				t.Fatal("old backend missing baseline was fabricated")
			}
			if name == "fallback" && (view.V1Only == false || view.Why != "自己的原因") {
				t.Fatalf("fallback metadata lost: %+v", view)
			}
		})
	}
}

// TestConflictRevisionIsForwarded proves the current revision survives the
// client error so the UI can explain the conflict.
func TestConflictRevisionIsForwarded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"revision_conflict","revision":7}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	_, err := client.ApplyV2Override(context.Background(), 1, V2Override{Field: "topics", Term: "llm", Action: "accept", OperationKey: "op"})
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || !apiErr.IsConflict() || apiErr.Revision == nil || *apiErr.Revision != 7 {
		t.Fatalf("conflict revision not forwarded: %v", err)
	}
}

func asAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}
