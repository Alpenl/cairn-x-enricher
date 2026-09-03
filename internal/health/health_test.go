package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDashboard(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	NewTracker().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("GET / Content-Type = %q", got)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("GET / is missing Content-Security-Policy")
	}
	body := response.Body.String()
	for _, want := range []string{"Cairn X Enricher", "fetch(\"/status\"", "Latest batch"} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET / body does not contain %q", want)
		}
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
