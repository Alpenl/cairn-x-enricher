package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// countingQueue records how many batches attempted a claim.
type countingQueue struct {
	calls atomic.Int64
}

func (q *countingQueue) Claim(ctx context.Context) (*cairn.Job, error) {
	q.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *countingQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}

func (q *countingQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}

func (q *countingQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }

func (q *countingQueue) Fail(context.Context, int64, string, string) error { return nil }

// blockingQueue signals when a claim begins so the test can cancel shutdown
// while a batch is genuinely in flight.
type blockingQueue struct {
	started chan struct{}
	once    *sync.Once
}

func (q *blockingQueue) Claim(ctx context.Context) (*cairn.Job, error) {
	q.once.Do(func() { close(q.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *blockingQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}

func (q *blockingQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}

func (q *blockingQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }

func (q *blockingQueue) Fail(context.Context, int64, string, string) error { return nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

type noopEnricher struct{}

func (noopEnricher) Enrich(context.Context, enrich.Input) (enrich.Result, error) {
	return enrich.Result{}, nil
}

func TestReadinessReasonExplainsFailures(t *testing.T) {
	reason := readinessReason(strings.NewReader(`{"ready":false,"ready_reason":"starting"}`))
	if reason != "starting" {
		t.Fatalf("readinessReason() = %q", reason)
	}
	if got := readinessReason(strings.NewReader("not json")); got != "not ready" {
		t.Fatalf("readinessReason(invalid) = %q", got)
	}
	if got := readinessReason(strings.NewReader(`{"ready":true}`)); got != "not ready" {
		t.Fatalf("readinessReason(empty reason) = %q", got)
	}
}

func TestHealthcheckCommandTargetsLivenessByDefault(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	root := newRootCommand()
	root.SetArgs([]string{"healthcheck", "--url", server.URL + "/healthz"})
	if err := root.Execute(); err != nil {
		t.Fatalf("healthcheck error = %v", err)
	}
	if gotPath != "/healthz" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestHealthcheckCommandFailsOnUnreadyService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"ready":false,"ready_reason":"model endpoint contract check failed"}`))
	}))
	defer server.Close()

	root := newRootCommand()
	root.SetArgs([]string{"healthcheck", "--ready", "--url", server.URL + "/readyz"})
	err := root.Execute()
	if err == nil {
		t.Fatal("healthcheck error = nil, want an unready failure")
	}
	if !strings.Contains(err.Error(), "contract check failed") {
		t.Fatalf("healthcheck error = %v, want the readiness reason", err)
	}
}

func TestSchedulerDrainsTheInFlightBatchOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tracker := health.NewTracker()
	tracker.MarkStarted()

	started := make(chan struct{})
	var once sync.Once
	queue := &blockingQueue{started: started, once: &once}
	worker := processor.New(queue, &noopEnricher{}, discardLogger(), 1)
	cfg := config.Config{MaxJobsPerRun: 1, PollInterval: time.Hour, ShutdownTimeout: 2 * time.Second}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduler(ctx, worker, tracker, cfg, discardLogger())
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler never started a batch")
	}

	// Signal shutdown while the batch is in flight. The batch must be given
	// the shutdown budget to finish rather than being cancelled instantly.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runScheduler did not return after shutdown")
	}
}

func TestSchedulerStopsAdmittingNewBatchesAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tracker := health.NewTracker()
	tracker.MarkStarted()

	queue := &countingQueue{}
	worker := processor.New(queue, &noopEnricher{}, discardLogger(), 1)
	cfg := config.Config{MaxJobsPerRun: 1, PollInterval: time.Millisecond, ShutdownTimeout: time.Second}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduler(ctx, worker, tracker, cfg, discardLogger())
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runScheduler did not stop")
	}
	after := queue.calls.Load()

	// No new batches may start once shutdown has been observed.
	time.Sleep(50 * time.Millisecond)
	if got := queue.calls.Load(); got != after {
		t.Fatalf("batches continued after shutdown: %d -> %d", after, got)
	}
}
