package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestHandlerAndRunState(t *testing.T) {
	tracker := NewTracker()
	for _, path := range []string{"/healthz", "/readyz", "/status"} {
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
