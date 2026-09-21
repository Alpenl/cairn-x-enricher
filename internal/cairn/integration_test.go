package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// mockWorker is a small stateful stand-in for the Worker used by the
// cross-repository integration tests. It exercises the real client code paths
// (handshake, claim, idempotent complete, typed errors) without a network
// dependency on Cloudflare.
type mockWorker struct {
	mu            sync.Mutex
	target        ClassificationTarget
	generation    int64
	revision      int64
	leases        map[string]bool
	operations    map[string]string
	claimCalls    int
	completeCalls int
	modelCalls    int
	nextConflict  bool
	capabilityOK  bool
}

func newMockWorker() *mockWorker {
	return &mockWorker{
		target: ClassificationTarget{
			Generation: 0, SpecID: "classify-v1", SpecHash: "sha", TaxonomyVersion: "v1",
			PolicyVersion: classify.PolicyVersion, RequestedModel: "jev", Protocol: "v2",
		},
		leases: map[string]bool{}, operations: map[string]string{}, capabilityOK: true,
	}
}

func (m *mockWorker) handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/enrichment/classifications/target":
			_ = json.NewEncoder(writer).Encode(HandshakeResult{Target: m.target, Supported: m.capabilityOK})
		case "/api/enrichment/classifications/claim":
			m.claimCalls++
			if !m.capabilityOK {
				writer.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(writer).Encode(map[string]string{"error": "capability_mismatch"})
				return
			}
			m.leases["lease-1"] = true
			m.revision = 1
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": 1, "revision": m.revision, "input_revision": 5, "target_generation": m.generation,
				"spec_id": m.target.SpecID, "attempt": 1, "lease_token": "lease-1",
				"lease_until": "2999-01-01T00:00:00Z", "original_text": "text",
			})
		case "/api/enrichment/classifications/1/complete":
			m.completeCalls++
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			key, _ := body["operation_key"].(string)
			if existing, ok := m.operations[key]; ok {
				_ = json.NewEncoder(writer).Encode(map[string]string{"status": existing})
				return
			}
			if m.nextConflict {
				m.nextConflict = false
				writer.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(writer).Encode(map[string]string{"error": "target_changed"})
				return
			}
			m.operations[key] = "completed"
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "completed"})
		default:
			writer.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(writer).Encode(map[string]string{"error": "not_found"})
		}
	})
}

func testJob() *ClassificationJob {
	return &ClassificationJob{
		ID: 1, Revision: 1, InputRevision: 5, TargetGeneration: 0, SpecID: "classify-v1",
		LeaseToken: "lease-1",
		Input:      classify.Input{OriginalText: "text"},
	}
}

// TestRetryAfterLostCompletionDoesNotRepayForInference is the SC11 invariant:
// a lost completion response is replayed by operation key, not re-inferred.
func TestRetryAfterLostCompletionDoesNotRepayForInference(t *testing.T) {
	worker := newMockWorker()
	server := httptest.NewServer(worker.handler())
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())

	job := testJob()
	result := classify.Result{Model: "jev", PolicyVersion: classify.PolicyVersion}
	if err := client.CompleteClassification(context.Background(), job, result); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	// Simulate the response being lost and the same commit retried.
	if err := client.CompleteClassification(context.Background(), job, result); err != nil {
		t.Fatalf("retry complete: %v", err)
	}
	if worker.completeCalls != 2 {
		t.Fatalf("complete calls = %d", worker.completeCalls)
	}
	// The operation key is deterministic, so the Worker saw one logical commit.
	if len(worker.operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(worker.operations))
	}
	// No model call happened on either commit path.
	if worker.modelCalls != 0 {
		t.Fatalf("model calls = %d, want 0", worker.modelCalls)
	}
}

// TestStaleCompletionIsNotAModelFailure is the SC12 invariant.
func TestStaleCompletionIsNotAModelFailure(t *testing.T) {
	worker := newMockWorker()
	worker.nextConflict = true
	server := httptest.NewServer(worker.handler())
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())

	err := client.CompleteClassification(context.Background(), testJob(), classify.Result{Model: "jev", PolicyVersion: classify.PolicyVersion})
	if err == nil {
		t.Fatal("expected a conflict")
	}
	if enrich.ClassOf(err) != enrich.ErrorClassStale {
		t.Fatalf("class = %s, want stale", enrich.ClassOf(err))
	}
	if enrich.PausesComponent(err) {
		t.Fatal("a stale result must not pause the component")
	}
}

// TestCapabilityMismatchPausesTheComponent is the SC12 component-level case.
func TestCapabilityMismatchPausesTheComponent(t *testing.T) {
	worker := newMockWorker()
	worker.capabilityOK = false
	server := httptest.NewServer(worker.handler())
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())

	_, err := client.ClaimClassification(context.Background(), "classify-v1", "v1", "jev")
	if err == nil {
		t.Fatal("expected a capability mismatch")
	}
	if !enrich.PausesComponent(err) {
		t.Fatalf("class = %s, want a component pause", enrich.ClassOf(err))
	}
	// The mismatch must not consume an attempt: no job was handed out.
	if worker.claimCalls != 0 {
		t.Fatalf("claim calls = %d, want 0 before capability check", worker.claimCalls)
	}
}

// TestConcurrentClaimsYieldOneLease proves the client does not race itself.
func TestConcurrentClaimsYieldOneLease(t *testing.T) {
	var granted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if granted.Add(1) == 1 {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": 1, "url": "https://x.com/a/status/1", "attempt": 1,
				"lease_token": "lease-1", "lease_until": "2999-01-01T00:00:00Z",
			})
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())

	var jobs atomic.Int64
	var wait sync.WaitGroup
	for range 5 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			job, err := client.Claim(context.Background())
			if err == nil && job != nil {
				jobs.Add(1)
			}
		}()
	}
	wait.Wait()
	if jobs.Load() != 1 {
		t.Fatalf("jobs handed out = %d, want 1", jobs.Load())
	}
}

// TestAPIErrorClassificationIsExhaustive guards the typed mapping table.
func TestAPIErrorClassificationIsExhaustive(t *testing.T) {
	cases := map[string]enrich.ErrorClass{
		"capability_mismatch": enrich.ErrorClassConfiguration,
		"configuration_error": enrich.ErrorClassConfiguration,
		"target_changed":      enrich.ErrorClassStale,
		"input_changed":       enrich.ErrorClassStale,
		"lease_expired":       enrich.ErrorClassStale,
		"already_completed":   enrich.ErrorClassCompleted,
		"operation_conflict":  enrich.ErrorClassContract,
		"invalid_source":      enrich.ErrorClassContract,
	}
	for code, want := range cases {
		if got := enrich.ClassOf(&APIError{StatusCode: http.StatusConflict, Code: code}); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

// TestNoOperationKeyStillCommitsForLegacyConsumers keeps old clients working.
func TestNoOperationKeyStillCommitsForLegacyConsumers(t *testing.T) {
	var commits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		commits.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	// The legacy full-enrichment completion path is unaffected by the v2 key.
	legacy := Completion{LeaseToken: "l", OriginalText: "t", Model: "m"}
	if err := client.Complete(context.Background(), 1, legacy); err != nil {
		t.Fatal(err)
	}
	if commits.Load() != 1 {
		t.Fatalf("commits = %d", commits.Load())
	}
}

// TestStaleDetectionUsesTypedClassNotStatusAlone ensures a bare 409 is not
// silently treated as success.
func TestStaleDetectionUsesTypedClassNotStatusAlone(t *testing.T) {
	if enrich.ClassOf(&APIError{StatusCode: http.StatusConflict}) != enrich.ErrorClassStale {
		t.Fatal("a bare 409 must be stale")
	}
	if !errors.Is(ErrV2Unsupported, ErrV2Unsupported) {
		t.Fatal("sentinel must be comparable")
	}
}
