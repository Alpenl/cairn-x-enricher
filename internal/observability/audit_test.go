package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlAuditSurvivesOffRestartAndExpiry(t *testing.T) {
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
	log := store.Audit()
	if log.SchemaVersion != 1 || len(log.Entries) != 4 || log.TruncatedBeforeVersion != 0 {
		t.Fatalf("control history = %+v", log)
	}
	if log.Entries[0].Result != "started" || log.Entries[1].Result != "applied" ||
		log.Entries[0].Mode != LogOff || log.Entries[0].Actor != "container_loopback" ||
		log.Entries[3].Result != "applied" || !log.Entries[3].DiagnosticUntil.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("control history has wrong transition: %+v", log.Entries)
	}
	info, err := os.Stat(path + ".audit.json")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private audit file: %v %v", info, err)
	}
	reloaded, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = store.now
	if got := reloaded.Audit(); len(got.Entries) != 4 {
		t.Fatalf("restart lost control history: %+v", got)
	}
	now = now.Add(15 * time.Minute)
	if reloaded.Snapshot().EffectiveLogs != LogOff {
		t.Fatal("diagnostic expiry did not restore off")
	}
	now = now.Add(15 * 24 * time.Hour)
	if err := reloaded.PruneAudit(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Audit(); len(got.Entries) != 0 || got.TruncatedBeforeVersion != 2 {
		t.Fatalf("expired control history remains: %+v", got)
	}
}

func TestAuditFailureCannotSilentlyChangePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".audit.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".audit.json", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(0, LogOff, 0); err == nil {
		t.Fatal("unwritable audit accepted a policy change")
	}
	if got := store.Snapshot(); got.AppliedVersion != 0 || got.EffectiveLogs != LogBasic || got.ControlAuditErrors != 1 {
		t.Fatalf("failed audit changed policy or hid its failure: %+v", got)
	}
}

func TestCorruptAuditKeepsBusinessLoggerRunningButDisablesControlWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	if _, err := Open(path, slog.LevelInfo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".audit.json", []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatalf("optional audit stopped the service: %v", err)
	}
	if got := store.Snapshot(); got.ControlAuditAvailable || got.ControlAuditErrors != 1 {
		t.Fatalf("bad audit was hidden: %+v", got)
	}
	var output bytes.Buffer
	store.Logger(&output).Info("service-still-runs")
	if !strings.Contains(output.String(), "service-still-runs") {
		t.Fatal("business logger did not survive audit corruption")
	}
	if _, err := store.Update(0, LogOff, 0); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("control write bypassed unavailable audit: %v", err)
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPut,
		"http://127.0.0.1:9090/v1/observability/logs", strings.NewReader(`{"expected_version":0,"mode":"off"}`))
	request.RemoteAddr = "127.0.0.1:1000"
	response := httptest.NewRecorder()
	store.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"error":"audit_unavailable"`) {
		t.Fatalf("control API claimed an unconfirmed write: %d %s", response.Code, response.Body.String())
	}
}

func TestPolicyFailureLeavesHonestAuditResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(0, LogOff, 0); err == nil {
		t.Fatal("policy persistence failure was accepted")
	}
	if got := store.Snapshot(); got.AppliedVersion != 0 || got.EffectiveLogs != LogBasic {
		t.Fatalf("failed policy changed live mode: %+v", got)
	}
	entries := store.Audit().Entries
	if len(entries) != 2 || entries[0].Result != "started" || entries[1].Result != "rejected" {
		t.Fatalf("failed policy was not audited: %+v", entries)
	}
}

func TestControlAuditEndpointStaysLocalAndExcludesPrivateInputs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(0, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	request := func(peer string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:9090/v1/observability/audit", nil)
		r.RemoteAddr = peer
		response := httptest.NewRecorder()
		store.Handler().ServeHTTP(response, r)
		return response
	}
	if response := request("192.0.2.10:1000"); response.Code != http.StatusForbidden {
		t.Fatalf("remote audit read = %d", response.Code)
	}
	response := request("127.0.0.1:1000")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "original_text") ||
		strings.Contains(response.Body.String(), "operation_id") || strings.Contains(response.Body.String(), "prompt") {
		t.Fatalf("unsafe audit response: %d %s", response.Code, response.Body.String())
	}
	var decoded ControlAudit
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || len(decoded.Entries) != 2 {
		t.Fatalf("audit JSON: %+v %v", decoded, err)
	}
}

func TestControlAuditCapacityReportsHistoryGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	store, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store.audit.Entries = make([]ControlAuditEntry, 2200)
	for index := range store.audit.Entries {
		store.audit.Entries[index] = ControlAuditEntry{At: now, Version: uint64(index + 1),
			Signal: "logs", Mode: LogBasic, Actor: "container_loopback", Result: "applied"}
	}
	if err := store.appendAudit(ControlAuditEntry{At: now, Version: 2201,
		Signal: "logs", Mode: LogOff, Actor: "container_loopback", Result: "applied"}); err != nil {
		t.Fatal(err)
	}
	got := store.Audit()
	if got.TruncatedBeforeVersion == 0 || len(got.Entries) == 0 || got.Entries[len(got.Entries)-1].Version != 2201 {
		t.Fatalf("bounded audit did not mark the gap: first removed=%d, retained=%d", got.TruncatedBeforeVersion, len(got.Entries))
	}
	info, err := os.Stat(path + ".audit.json")
	if err != nil || info.Size() > controlAuditMaxBytes {
		t.Fatalf("audit file exceeds cap: %v %v", info, err)
	}
	reloaded, err := Open(path, slog.LevelInfo)
	if err != nil || reloaded.Audit().TruncatedBeforeVersion != got.TruncatedBeforeVersion {
		t.Fatalf("restart lost the truncation marker: %v", err)
	}
}

func TestAuditRejectsUnexpectedStoredFieldsBeforeServingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observability.json")
	if _, err := Open(path, slog.LevelInfo); err != nil {
		t.Fatal(err)
	}
	stored := `{"schema_version":1,"entries":[{"at":"2026-09-28T00:00:00Z","version":1,` +
		`"signal":"logs","mode":"off","actor":"private-bookmark-id","result":"applied"}]}`
	if err := os.WriteFile(path+".audit.json", []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, slog.LevelInfo)
	if err != nil || store.Snapshot().ControlAuditAvailable {
		t.Fatalf("unsafe stored control history became available: %v", err)
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://127.0.0.1:9090/v1/observability/audit", nil)
	request.RemoteAddr = "127.0.0.1:1000"
	response := httptest.NewRecorder()
	store.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private-bookmark-id") {
		t.Fatalf("unsafe audit leaked: %d %s", response.Code, response.Body.String())
	}
}
