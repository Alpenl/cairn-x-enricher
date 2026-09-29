package cairn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClassificationFailureSendsOnlyBoundedRetryHint(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/enrichment/classifications/7/fail" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode failure report: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	job := &ClassificationJob{ID: 7, LeaseToken: "lease", Revision: 2, InputRevision: 3}
	if err := client.FailClassification(context.Background(), job, "HTTP 529", 24*time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if err := client.FailClassification(context.Background(), job, "superseded", 0, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0]["retry_after_ms"] != float64(600_000) ||
		bodies[0]["component_fault"] != "provider_transient" {
		t.Fatalf("bounded retry hint missing: %v", bodies)
	}
	if _, present := bodies[1]["retry_after_ms"]; present {
		t.Fatalf("zero retry hint should be omitted: %v", bodies[1])
	}
	if _, present := bodies[1]["component_fault"]; present {
		t.Fatalf("superseded work cannot open a component gate: %v", bodies[1])
	}
}
