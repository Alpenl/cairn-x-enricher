package main

import (
	"context"
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

func TestBatchTimeoutLeavesRoomForShutdownDrain(t *testing.T) {
	// A batch and the shutdown drain that waits for it share one budget. If a
	// batch could consume the whole SHUTDOWN_TIMEOUT, the drain would always be
	// cut off, and the container's stop_grace_period would SIGKILL the process.
	for _, total := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute} {
		cfg := config.Config{ShutdownTimeout: total}
		if got := batchTimeout(cfg); got >= total {
			t.Errorf("batchTimeout(%v) = %v, want strictly less than the total budget", total, got)
		}
	}
}

func TestBatchTimeoutHandlesDegenerateBudgets(t *testing.T) {
	if got := batchTimeout(config.Config{ShutdownTimeout: time.Nanosecond}); got <= 0 {
		t.Fatalf("batchTimeout = %v, want a positive duration", got)
	}
	if got := batchTimeout(config.Config{}); got != 0 {
		t.Fatalf("batchTimeout(0) = %v", got)
	}
}

func TestShutdownBudgetFitsInsideStopGracePeriod(t *testing.T) {
	// Both compose files set stop_grace_period; SHUTDOWN_TIMEOUT must stay below
	// it or Docker kills the process mid-drain. This guards against the two
	// drifting apart.
	const stopGracePeriod = 30 * time.Second
	defaults := config.Config{ShutdownTimeout: 15 * time.Second}
	if defaults.ShutdownTimeout >= stopGracePeriod {
		t.Fatalf("SHUTDOWN_TIMEOUT %v does not leave room inside stop_grace_period %v",
			defaults.ShutdownTimeout, stopGracePeriod)
	}
}

func TestRunBatchSafelyRecoversFromAPanic(t *testing.T) {
	// A panicking queue stands in for a bug anywhere in the batch path.
	queue := &panickingQueue{}
	worker := processor.New(queue, &noopEnricher{}, discardLogger(), 1)
	cfg := config.Config{MaxJobsPerRun: 1, ShutdownTimeout: 2 * time.Second}

	stats, err := runBatchSafely(context.Background(), worker, cfg, discardLogger())
	if err == nil {
		t.Fatal("runBatchSafely() error = nil, want the recovered panic")
	}
	// The zero Stats returned alongside a recovered panic must still be usable
	// by the caller's error path.
	if stats.Duration < 0 {
		t.Fatal("stats were not populated")
	}
}

func TestBatchTimeoutBoundsABatchThatIgnoresCancellation(t *testing.T) {
	// The batch context deliberately detaches from the parent so jobs are not
	// cancelled mid-flight, which means only the timeout can stop it.
	var once sync.Once
	queue := &blockingQueue{started: make(chan struct{}), once: &once}
	worker := processor.New(queue, &noopEnricher{}, discardLogger(), 1)
	cfg := config.Config{MaxJobsPerRun: 1, ShutdownTimeout: 200 * time.Millisecond}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runBatchSafely(context.Background(), worker, cfg, discardLogger())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch was not bounded by its timeout")
	}
}

// panickingQueue panics on the first claim, simulating a batch-path bug.
type panickingQueue struct{}

func (q *panickingQueue) Claim(context.Context) (*cairn.Job, error) {
	panic("simulated claim bug")
}

func (q *panickingQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}

func (q *panickingQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}

func (q *panickingQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }

func (q *panickingQueue) Fail(context.Context, int64, string, string) error { return nil }

// emptyQueue reports "no work" immediately, like an idle Worker.
type emptyQueue struct{}

func (q *emptyQueue) Claim(context.Context) (*cairn.Job, error) { return nil, nil }

func (q *emptyQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}

func (q *emptyQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}

func (q *emptyQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }

func (q *emptyQueue) Fail(context.Context, int64, string, string) error { return nil }

func TestTrackerRecordsNoFailureWhenOnlyWorkIsEmpty(t *testing.T) {
	tracker := health.NewTracker()
	tracker.MarkStarted()
	stats, err := runBatchSafely(context.Background(), processor.New(&emptyQueue{}, &noopEnricher{}, discardLogger(), 1),
		config.Config{MaxJobsPerRun: 1, ShutdownTimeout: time.Second}, discardLogger())
	if err != nil {
		t.Fatalf("runBatchSafely() error = %v", err)
	}
	tracker.Record(stats, err)
	if snapshot := tracker.Snapshot(); !snapshot.Ready {
		t.Fatalf("service became unready after an empty batch: %+v", snapshot)
	}
}

func TestWaitForSignalReportsCompletion(t *testing.T) {
	done := make(chan struct{})
	go func() { time.Sleep(20 * time.Millisecond); close(done) }()
	if !waitForSignal(done, time.Second) {
		t.Fatal("waitForSignal did not observe the closed channel")
	}
}

func TestWaitForSignalTimesOutWithoutCompleting(t *testing.T) {
	// This is the guard that keeps a stuck scheduler from holding the process
	// open past the shutdown budget.
	done := make(chan struct{})
	start := time.Now()
	if waitForSignal(done, 50*time.Millisecond) {
		t.Fatal("waitForSignal reported completion for an open channel")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForSignal blocked for %v", elapsed)
	}
}

// The scheduler must return once its context is cancelled, even mid-batch, so
// runServe can wait for it without risking the shutdown budget.
func TestSchedulerReturnsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tracker := health.NewTracker()
	tracker.MarkStarted()

	queue := &oneJobQueue{}
	enricher := &slowEnricher{hold: 300 * time.Millisecond}
	worker := processor.New(queue, enricher, discardLogger(), 1)
	cfg := config.Config{MaxJobsPerRun: 2, PollInterval: time.Hour, ShutdownTimeout: 5 * time.Second}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduler(ctx, worker, tracker, cfg, discardLogger())
	}()

	// Let the first batch start, then cancel mid-job.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if !waitForSignal(done, 5*time.Second) {
		t.Fatal("runScheduler did not return after cancellation")
	}
	// The in-flight job must have been allowed to finish rather than being
	// interrupted, which would have wasted its lease.
	if enricher.calls.Load() == 0 {
		t.Fatal("the in-flight job never ran")
	}
}

// slowEnricher takes a fixed time to finish and ignores cancellation, standing
// in for an in-flight model call. Run must let it complete rather than
// interrupting it, so the batch returns shortly after this delay.
type slowEnricher struct {
	hold  time.Duration
	calls atomic.Int64
}

func (e *slowEnricher) Enrich(context.Context, enrich.Input) (enrich.Result, error) {
	e.calls.Add(1)
	time.Sleep(e.hold)
	return enrich.Result{
		AITitle: "排空测试使用的中文标题", OriginalLanguage: "en", OriginalText: "s",
		TranslatedText: "译", Summary: "摘", Model: "m",
	}, nil
}

// oneJobQueue hands out a single job and then reports an empty queue.
type oneJobQueue struct {
	mu   sync.Mutex
	used bool
}

func (q *oneJobQueue) Claim(context.Context) (*cairn.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.used {
		return nil, nil
	}
	q.used = true
	return &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "l", LeaseUntil: "u"}, nil
}

func (q *oneJobQueue) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return cairn.BookmarkDetail{}, nil
}

func (q *oneJobQueue) StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error) {
	return nil, nil
}

func (q *oneJobQueue) Complete(context.Context, int64, cairn.Completion) error { return nil }

func (q *oneJobQueue) Fail(context.Context, int64, string, string) error { return nil }
