package main

import (
	"bytes"
	"log/slog"
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
