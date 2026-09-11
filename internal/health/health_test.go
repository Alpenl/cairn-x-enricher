package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestHandlerAndRunState(t *testing.T) {
	tracker := NewTracker()
	// Liveness must not depend on upstream health: a process that cannot
	// reach the Worker has to stay up so it can recover.
	for _, path := range []string{"/healthz", "/status"} {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		tracker.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.Code)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q", path, response.Header().Get("Cache-Control"))
		}
	}

	stats := processor.Stats{Claimed: 1, Failed: 1}
	tracker.Record(stats, errors.New("backend unavailable"))
	snapshot := tracker.Snapshot()
	if snapshot.LastRunAt == nil || snapshot.LastError != "backend unavailable" || snapshot.LastStats == nil {
		t.Fatalf("Snapshot() = %+v", snapshot)
	}
}

func TestTrackerStartsUnreadyUntilStartupCompletes(t *testing.T) {
	tracker := NewTracker()
	if tracker.Ready() {
		t.Fatal("Ready() = true before startup prerequisites completed")
	}
	if got := tracker.Snapshot().ReadyReason; got != "starting" {
		t.Fatalf("ReadyReason = %q, want starting", got)
	}

	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	tracker.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d, want 503", response.Code)
	}

	tracker.MarkStarted()
	response = httptest.NewRecorder()
	tracker.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /readyz after MarkStarted status = %d, want 200", response.Code)
	}
	body := map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readiness body: %v", err)
	}
	if ready, ok := body["ready"].(bool); !ok || !ready {
		t.Fatalf("readiness body = %#v", body)
	}
}

func TestTrackerStaysUnreadyWhileDegraded(t *testing.T) {
	tracker := NewTracker()
	tracker.MarkStarted()
	if !tracker.Ready() {
		t.Fatal("Ready() = false after MarkStarted")
	}

	tracker.MarkDegraded("model API returned HTTP 401")
	if tracker.Ready() {
		t.Fatal("Ready() = true while degraded")
	}
	snapshot := tracker.Snapshot()
	if snapshot.ReadyReason != "model API returned HTTP 401" || snapshot.UnhealthySince == nil {
		t.Fatalf("Snapshot() = %+v", snapshot)
	}

	// A failed batch alone must not silently clear a degraded state.
	tracker.Record(processor.Stats{Claimed: 1, Failed: 1}, errors.New("still failing"))
	if tracker.Ready() {
		t.Fatal("Ready() = true after a failing batch while degraded")
	}

	tracker.MarkStarted()
	if !tracker.Ready() {
		t.Fatal("Ready() = false after recovery")
	}
	if tracker.Snapshot().UnhealthySince != nil {
		t.Fatal("UnhealthySince was not cleared on recovery")
	}
}

func TestTrackerFailureDoesNotFlipLiveness(t *testing.T) {
	tracker := NewTracker()
	tracker.MarkStarted()
	tracker.Record(processor.Stats{Claimed: 1, Failed: 1}, errors.New("boom"))

	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	tracker.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", response.Code)
	}
	if !tracker.Snapshot().Ready {
		t.Fatal("a transient batch failure made the service unready")
	}
}

func TestTrackerKeepsLastWorkStatsAcrossEmptyBatches(t *testing.T) {
	tracker := NewTracker()
	tracker.Record(processor.Stats{Claimed: 2, Completed: 2}, nil)
	tracker.Record(processor.Stats{}, nil)

	snapshot := tracker.Snapshot()
	if snapshot.LastStats == nil || snapshot.LastStats.Claimed != 0 {
		t.Fatalf("LastStats = %+v, want latest empty batch", snapshot.LastStats)
	}
	if snapshot.LastWorkStats == nil || snapshot.LastWorkStats.Claimed != 2 || snapshot.LastWorkStats.Completed != 2 {
		t.Fatalf("LastWorkStats = %+v, want last non-empty batch", snapshot.LastWorkStats)
	}
}

func TestTrackerUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker := NewTrackerWithClock(func() time.Time { return now })
	if tracker.Snapshot().StartedAt != now {
		t.Fatalf("StartedAt = %v, want %v", tracker.Snapshot().StartedAt, now)
	}
}

func TestHandlerReturnsNotFoundForUnknownPath(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/missing", nil)
	response := httptest.NewRecorder()
	NewTracker().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /missing status = %d", response.Code)
	}
}
