package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestAsyncLoggerDrainsStructuredRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, closeExporter, err := store.AsyncLogger(&output, 4)
	if err != nil {
		t.Fatal(err)
	}
	logger.WithGroup("task").With("component", "source").Info("manual source persisted", "link_id", 7)
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, `"msg":"manual source persisted"`) ||
		!strings.Contains(got, `"task":{"component":"source"}`) || strings.Contains(got, "link_id") {
		t.Fatalf("structured record was not drained: %s", got)
	}
	if status := store.Snapshot().LogExporter; status == nil || !status.Closed ||
		status.Dropped != 0 || status.WriteErrors != 0 || status.QueueDepth != 0 {
		t.Fatalf("exporter status after drain = %+v", status)
	}
	logger.Info("after-close")
	if strings.Contains(output.String(), "after-close") {
		t.Fatal("closed exporter accepted another record")
	}
}

func TestPaidAttemptEventsKeepOnlySafeDimensions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, closeExporter, err := store.AsyncLogger(&output, 4)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("provider attempt", "schema_version", 1,
		"event_name", "provider_attempt_responded", "stage", "fetch",
		"provider_variant", "fetch_post", "provider_http_status", 200,
		"cost_usd_ticks", 1234, "operation_key", "private-operation",
		"response_id", "private-response", "prompt", "private-prompt")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := output.String()
	for _, want := range []string{`"event_name":"provider_attempt_responded"`,
		`"provider_variant":"fetch_post"`, `"cost_usd_ticks":1234`} {
		if !strings.Contains(log, want) {
			t.Fatalf("safe paid event field %q missing: %s", want, log)
		}
	}
	if strings.Contains(log, "private-") || strings.Contains(log, "operation_key") ||
		strings.Contains(log, "response_id") || strings.Contains(log, "prompt") {
		t.Fatalf("private paid event field leaked: %s", log)
	}
}

func TestLocalStageEventRespectsHotOffAndPrivateFieldFilter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, closeExporter, err := store.AsyncLogger(&output, 4)
	if err != nil {
		t.Fatal(err)
	}
	logger.Warn("source stage event", "event_name", "local_stage_paused", "stage", "source",
		"error_class", "configuration", "backoff_ms", 30_000,
		"lease_token", "private-lease", "error", errors.New("private provider body"))
	if _, err := store.Update(0, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	logger.Warn("source stage event", "event_name", "local_defer_failed", "stage", "source")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if !strings.Contains(log, `"event_name":"local_stage_paused"`) ||
		!strings.Contains(log, `"backoff_ms":30000`) ||
		strings.Contains(log, `"event_name":"local_defer_failed"`) ||
		strings.Contains(log, "private-") || strings.Contains(log, "lease_token") {
		t.Fatalf("local stage event violated policy or privacy filter: %s", log)
	}
}

type blockedWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *blockedWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}

func TestAsyncLoggerDropsWhenWriterBlocksAndCloseIsBounded(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	writer := &blockedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	logger, closeExporter, err := store.AsyncLogger(writer, 1)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("first")
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("exporter never entered the writer")
	}
	completed := make(chan struct{})
	go func() {
		logger.Info("second")
		logger.Info("third")
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("blocked writer stalled a business log call")
	}
	if got := store.Snapshot().LogExporter; got == nil || got.QueueDepth != 1 || got.Dropped != 1 {
		t.Fatalf("blocked exporter status = %+v", got)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := closeExporter(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded close = %v, want deadline exceeded", err)
	}
	close(writer.release)
	finish, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := closeExporter(finish); err != nil {
		t.Fatalf("drain after unblocking: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestAsyncLoggerCountsWriterFailureAndRespectsOff(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	logger, closeExporter, err := store.AsyncLogger(failingWriter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("one")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().LogExporter; got == nil || got.WriteErrors != 1 {
		t.Fatalf("writer failure was not counted: %+v", got)
	}
	other, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Update(0, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	quiet, closeQuiet, err := other.AsyncLogger(&output, 2)
	if err != nil {
		t.Fatal(err)
	}
	quiet.Debug("debug")
	quiet.Error("error")
	if err := closeQuiet(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("off mode emitted %q", output.String())
	}
}

func TestAsyncLoggerOnlyExportsSafeFieldsAndErrorClasses(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, closeExporter, err := store.AsyncLogger(&output, 8)
	if err != nil {
		t.Fatal(err)
	}
	private := "private-source-token-123"
	logger.With("source_text", private, "component", "source", "status", private).Info("manual source persisted",
		"link_id", 42, "prompt", private, "stack", private,
		"error", errors.New(private), "request_id", private,
		"reason", private, "error_code", private)
	logger.Warn("worker failed", "error", &cairn.APIError{StatusCode: 409, Code: private})
	logger.Warn("provider failed", "error", &enrich.ModelHTTPError{StatusCode: 502, Type: private})
	logger.WithGroup(private).With("component", "source").Info(private,
		"link_id", 73, "request_id", "123e4567-e89b-12d3-a456-426614174000")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{`"component":"source"`, `"msg":"application_event"`,
		`"error_code":"unknown"`, `"error_code":"worker_http_409_contract"`,
		`"error_code":"provider_http_502"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("safe field %s missing: %s", want, got)
		}
	}
	for _, secret := range []string{private, "source_text", "prompt", "stack", "link_id",
		"request_id", "123e4567-e89b-12d3-a456-426614174000", "status", "reason"} {
		if strings.Contains(got, secret) {
			t.Fatalf("private field %s leaked: %s", secret, got)
		}
	}
}

func TestAsyncLoggerFollowsLiveMode(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "observability.json"), slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	var output bytes.Buffer
	logger, closeExporter, err := store.AsyncLogger(&output, 8)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("enrichment started")
	logger.Info("scheduled batch finished")
	if _, err := store.Update(0, LogDiagnostic, time.Minute); err != nil {
		t.Fatal(err)
	}
	logger.Debug("classification round stopped")
	now = now.Add(time.Minute)
	logger.Debug("enrichment completed")
	logger.Info("manual source persisted")
	now = now.Add(time.Second)
	if _, err := store.Update(1, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	logger.Error("enrichment failed")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"scheduled batch finished", "classification round stopped", "manual source persisted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("live mode lost %s: %s", want, got)
		}
	}
	for _, omitted := range []string{"enrichment started", "enrichment completed", "enrichment failed"} {
		if strings.Contains(got, omitted) {
			t.Fatalf("live mode emitted %s: %s", omitted, got)
		}
	}
}
