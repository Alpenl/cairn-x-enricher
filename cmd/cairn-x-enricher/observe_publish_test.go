package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
)

func TestWorkerPolicyPublisherRetriesAndReportsConfirmedVersion(t *testing.T) {
	store, err := observability.Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/observability" || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected control request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 2 || body["logs"] != "basic" || body["version"] != float64(0) {
			t.Errorf("unsafe or incorrect policy body: %#v", body)
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"observability_unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":0,"effective_logs":"basic"}`))
	}))
	defer server.Close()
	client := cairn.NewClient(server.URL, "test-token", server.Client())
	publishWorkerPolicyOnce(context.Background(), store, client)
	if got := store.Snapshot(); got.WorkerPublishState != "unavailable" || got.WorkerPersistedVersion != nil {
		t.Fatalf("failure looked confirmed: %+v", got)
	}
	publishWorkerPolicyOnce(context.Background(), store, client)
	got := store.Snapshot()
	if got.WorkerPublishState != "confirmed" || got.WorkerPersistedVersion == nil ||
		*got.WorkerPersistedVersion != 0 || got.WorkerLastConfirmedAt == nil {
		t.Fatalf("retry did not confirm persistence: %+v", got)
	}
	publishWorkerPolicyOnce(context.Background(), store, client)
	if attempts != 2 {
		t.Fatalf("unchanged policy republished %d times", attempts)
	}
	if _, err := store.Update(0, observability.LogOff, 0); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot(); got.WorkerPublishState != "pending" {
		t.Fatalf("new local version looked confirmed: %+v", got)
	}
}
