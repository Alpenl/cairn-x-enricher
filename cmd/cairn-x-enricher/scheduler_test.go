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
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// countingQueue records how many batches attempted a claim.
type countingQueue struct {
	calls atomic.Int64
}

type emptyNotifiedQueue struct{ claims chan struct{} }

func (q *emptyNotifiedQueue) Claim(context.Context) (*cairn.Job, error) {
	q.claims <- struct{}{}
	return nil, nil
}
func (q *emptyNotifiedQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}
func (q *emptyNotifiedQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}
func (q *emptyNotifiedQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }
func (q *emptyNotifiedQueue) Fail(context.Context, int64, string, string) error       { return nil }

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

func TestSchedulerWakesAfterManualSourceSave(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &emptyNotifiedQueue{claims: make(chan struct{}, 2)}
	worker := processor.New(queue, &noopEnricher{}, discardLogger(), 1)
	tracker := health.NewTracker()
	tracker.MarkStarted()
	wakeup := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduler(ctx, worker, tracker, config.Config{MaxJobsPerRun: 1, PollInterval: time.Hour,
			ShutdownTimeout: time.Second}, discardLogger(), wakeup)
	}()
	select {
	case <-queue.claims:
	case <-time.After(time.Second):
		t.Fatal("initial claim did not run")
	}
	wakeup <- struct{}{}
	select {
	case <-queue.claims:
	case <-time.After(time.Second):
		t.Fatal("manual save did not wake scheduler")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

type independentStageQueue struct {
	processor.StageQueue
	sourceStarted         chan struct{}
	classificationClaimed chan struct{}
	startOnce             sync.Once
	claimOnce             sync.Once
}

func (q *independentStageQueue) Claim(ctx context.Context) (*cairn.Job, error) {
	q.startOnce.Do(func() { close(q.sourceStarted) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *independentStageQueue) ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error) {
	q.claimOnce.Do(func() { close(q.classificationClaimed) })
	return nil, nil
}

type idleClassifier struct{}

func (idleClassifier) SpecID() string { return "test-spec" }
func (idleClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	return classify.Result{}, nil
}

func TestClassificationSchedulerRunsWhileSourceClaimIsBlocked(t *testing.T) {
	queue := &independentStageQueue{sourceStarted: make(chan struct{}), classificationClaimed: make(chan struct{})}
	worker := processor.NewStaged(queue, nil, idleClassifier{}, "v1", "jev", discardLogger(), 1)
	tracker := health.NewTracker()
	tracker.MarkStarted()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduler(ctx, worker, tracker, config.Config{
			MaxJobsPerRun: 1, PollInterval: time.Hour, ShutdownTimeout: 2 * time.Second,
		}, discardLogger())
	}()
	select {
	case <-queue.sourceStarted:
	case <-time.After(time.Second):
		t.Fatal("source round did not start")
	}
	select {
	case <-queue.classificationClaimed:
	case <-time.After(time.Second):
		t.Fatal("source claim blocked the independent classification round")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("independent schedulers did not stop")
	}
}

type finiteSourceQueue struct {
	processor.Queue
	mu        sync.Mutex
	remaining int
}

func (q *finiteSourceQueue) Claim(context.Context) (*cairn.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.remaining == 0 {
		return nil, nil
	}
	q.remaining--
	return &cairn.Job{ID: int64(q.remaining + 1), URL: "https://x.com/a/status/1", LeaseToken: "lease"}, nil
}
func (*finiteSourceQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}
func (*finiteSourceQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}
func (*finiteSourceQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }
func (*finiteSourceQueue) Fail(context.Context, int64, string, string) error       { return nil }

func TestSourceSweepFollowsBacklogPastTheOldClaimWindow(t *testing.T) {
	queue := &finiteSourceQueue{remaining: 4}
	worker := processor.New(queue, &slowEnricher{hold: 70 * time.Millisecond}, discardLogger(), 1)
	worker.SetClaimTimeout(20 * time.Millisecond)
	stats, err := runSourceSweepSafely(context.Background(), worker, config.Config{
		MaxJobsPerRun: 1, ShutdownTimeout: 100 * time.Millisecond,
	}, discardLogger())
	if err != nil || stats.Claimed != 4 || stats.Completed != 4 || stats.Duration < 200*time.Millisecond {
		t.Fatalf("source sweep stopped at a batch window: stats=%+v err=%v", stats, err)
	}
}

type finiteClassificationQueue struct {
	processor.StageQueue
	mu        sync.Mutex
	remaining int
}

func (q *finiteClassificationQueue) ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.remaining == 0 {
		return nil, nil
	}
	q.remaining--
	return &cairn.ClassificationJob{ID: int64(q.remaining + 1)}, nil
}
func (*finiteClassificationQueue) CompleteClassification(context.Context, *cairn.ClassificationJob, classify.Result) error {
	return nil
}

type slowClassifier struct{ hold time.Duration }

func (slowClassifier) SpecID() string { return "test-spec" }
func (c slowClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	time.Sleep(c.hold)
	return classify.Result{}, nil
}

func TestClassificationRoundUsesPerJobDeadline(t *testing.T) {
	queue := &finiteClassificationQueue{remaining: 2}
	worker := processor.NewStaged(queue, nil, slowClassifier{hold: 70 * time.Millisecond}, "v1", "jev", discardLogger(), 1)
	stats, err := runClassificationSafely(context.Background(), worker, config.Config{
		MaxJobsPerRun: 2, ShutdownTimeout: 100 * time.Millisecond,
	}, discardLogger())
	if err != nil || stats.Classified != 2 || stats.Duration < 100*time.Millisecond {
		t.Fatalf("classification round inherited a short batch window: stats=%+v err=%v", stats, err)
	}
}
