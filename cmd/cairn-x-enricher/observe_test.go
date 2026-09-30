package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/observability"
)

func TestObserveCommandChangesTheLivePersistedLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	store, err := observability.Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(store.Handler())
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	command := func(args ...string) string {
		t.Helper()
		root := newRootCommand()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetArgs(append([]string{"observe", "--address", address}, args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	if got := command("show"); !strings.Contains(got, `"version":0`) || !strings.Contains(got, `"effective_logs":"basic"`) {
		t.Fatalf("initial control state = %s", got)
	}
	if got := command("set-log", "--mode", "off", "--expected-version", "0"); !strings.Contains(got, `"effective_logs":"off"`) {
		t.Fatalf("updated control state = %s", got)
	}
	if got := command("audit"); !strings.Contains(got, `"mode":"off"`) || !strings.Contains(got, `"result":"applied"`) {
		t.Fatalf("control audit unavailable while application logs are off: %s", got)
	}
	var output bytes.Buffer
	store.Logger(&output).Error("must-be-off")
	if output.Len() != 0 {
		t.Fatalf("off mode still logged: %s", output.String())
	}
	reloaded, err := observability.Open(path, slog.LevelInfo)
	if err != nil || reloaded.Snapshot().EffectiveLogs != observability.LogOff {
		t.Fatalf("new process did not restore off: %v", err)
	}
}

func TestObserveAuditDoesNotTruncateAFullResponse(t *testing.T) {
	const payload = 12 * 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", payload)))
	}))
	defer server.Close()
	var output bytes.Buffer
	err := callObserve(context.Background(), strings.TrimPrefix(server.URL, "http://"), http.MethodGet,
		"/v1/observability/audit", nil, &output)
	if err != nil || output.Len() != payload {
		t.Fatalf("audit output length = %d, error = %v", output.Len(), err)
	}
}
