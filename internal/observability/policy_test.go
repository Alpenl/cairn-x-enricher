package observability

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogSwitchPersistsAndExpiresWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // The test created this file under its private temporary directory.
	initial, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(initial, []byte(`"logs":"basic"`)) {
		t.Fatalf("initial policy was not persisted: %q %v", initial, err)
	}
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	var output bytes.Buffer
	logger := store.Logger(&output).With("component", "source")
	logger.Debug("before")
	if output.Len() != 0 {
		t.Fatalf("basic mode emitted debug: %s", output.String())
	}
	status, err := store.Update(0, LogDiagnostic, 15*time.Minute)
	if err != nil || status.Desired.Version != 1 || status.EffectiveLogs != LogDiagnostic {
		t.Fatalf("diagnostic update: %+v %v", status, err)
	}
	logger.Debug("during")
	if !strings.Contains(output.String(), `"msg":"during"`) {
		t.Fatalf("existing logger did not switch on: %s", output.String())
	}
	if _, err := store.Update(0, LogOff, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update = %v", err)
	}
	reloaded, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = store.now
	if got := reloaded.Snapshot(); got.EffectiveLogs != LogDiagnostic || got.AppliedVersion != 1 {
		t.Fatalf("restart lost configuration: %+v", got)
	}
	now = now.Add(15 * time.Minute)
	logger.Debug("expired")
	logger.Info("basic-again")
	if strings.Contains(output.String(), `"msg":"expired"`) || !strings.Contains(output.String(), `"msg":"basic-again"`) {
		t.Fatalf("diagnostic did not expire on the existing logger: %s", output.String())
	}
	if got := reloaded.Snapshot(); got.EffectiveLogs != LogBasic || got.Desired.Logs != LogDiagnostic {
		t.Fatalf("expiry was not distinguishable from desired state: %+v", got)
	}
	now = now.Add(time.Second)
	if _, err := store.Update(1, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	before := output.Len()
	logger.Error("after-off")
	if output.Len() != before {
		t.Fatalf("off mode emitted a record: %s", output.String()[before:])
	}
}

func TestFailedPolicyWriteKeepsAppliedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(0, LogOff, 0); err == nil {
		t.Fatal("missing persistent directory was accepted")
	}
	if got := store.Snapshot(); got.EffectiveLogs != LogBasic || got.AppliedVersion != 0 {
		t.Fatalf("failed write changed live policy: %+v", got)
	}
}

func TestDiagnosticFromOffRestoresOffAfterExpiryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.Update(0, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.Update(1, LogDiagnostic, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = store.now
	if got := reloaded.Snapshot(); got.EffectiveLogs != LogDiagnostic || got.Desired.FallbackLogs != LogOff {
		t.Fatalf("restart lost the off fallback: %+v", got)
	}
	now = now.Add(15 * time.Minute)
	if got := reloaded.Snapshot(); got.EffectiveLogs != LogOff {
		t.Fatalf("expired diagnostic reopened logging: %+v", got)
	}
	var output bytes.Buffer
	reloaded.Logger(&output).Error("must-stay-off")
	if output.Len() != 0 {
		t.Fatalf("expired diagnostic emitted a log: %s", output.String())
	}
}

func TestSavedDiagnosticCannotLastForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	if err := os.WriteFile(path, []byte(`{"version":3,"logs":"diagnostic","updated_at":"2026-09-28T00:00:00Z","diagnostic_until":"2036-09-28T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, slog.LevelInfo); err == nil {
		t.Fatal("unbounded saved diagnostic policy was accepted")
	}
}
