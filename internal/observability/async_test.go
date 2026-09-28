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
	logger.WithGroup("task").With("component", "source").Info("admitted", "link_id", 7)
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, `"msg":"admitted"`) ||
		!strings.Contains(got, `"task":{"component":"source","link_id":7}`) {
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
	logger.With("source_text", private, "component", "source", "status", private).Info("source checkpoint",
		"link_id", 42, "prompt", private, "stack", private,
		"error", errors.New(private), "request_id", private,
		"reason", private, "error_code", private)
	logger.Warn("worker failed", "error", &cairn.APIError{StatusCode: 409, Code: private})
	logger.Warn("provider failed", "error", &enrich.ModelHTTPError{StatusCode: 502, Type: private})
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{`"component":"source"`, `"link_id":42`,
		`"error_code":"unknown"`, `"error_code":"worker_http_409_stale"`,
		`"error_code":"provider_http_502"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("safe field %s missing: %s", want, got)
		}
	}
	for _, secret := range []string{private, "source_text", "prompt", "stack", "request_id", "status", "reason"} {
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
	logger.Debug("basic-debug")
	logger.Info("basic-info")
	if _, err := store.Update(0, LogDiagnostic, time.Minute); err != nil {
		t.Fatal(err)
	}
	logger.Debug("diagnostic-debug")
	now = now.Add(time.Minute)
	logger.Debug("expired-debug")
	logger.Info("expired-info")
	now = now.Add(time.Second)
	if _, err := store.Update(1, LogOff, 0); err != nil {
		t.Fatal(err)
	}
	logger.Error("off-error")
	if err := closeExporter(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"basic-info", "diagnostic-debug", "expired-info"} {
		if !strings.Contains(got, want) {
			t.Fatalf("live mode lost %s: %s", want, got)
		}
	}
	for _, omitted := range []string{"basic-debug", "expired-debug", "off-error"} {
		if strings.Contains(got, omitted) {
			t.Fatalf("live mode emitted %s: %s", omitted, got)
		}
	}
}
