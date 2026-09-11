package dashboard

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// panickingProcessor panics instead of processing, simulating a bug in the
// enrichment path reached from the manual queue.
type panickingProcessor struct {
	calls atomic.Int64
}

func (p *panickingProcessor) Process(context.Context, *cairn.Job) error {
	p.calls.Add(1)
	panic("simulated manual processing bug")
}

func (p *panickingProcessor) ProcessWithSource(context.Context, *cairn.Job, string) error {
	p.calls.Add(1)
	panic("simulated manual processing bug")
}

// safeBuffer collects log output written from worker goroutines. A plain
// strings.Builder would race with the reading test goroutine.
type safeBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// capturingDashboardLogger records structured log output for assertions.
func capturingDashboardLogger() (*slog.Logger, *safeBuffer) {
	buffer := &safeBuffer{}
	return slog.New(slog.NewTextHandler(buffer, nil)), buffer
}

func TestManualWorkerSurvivesAPanicAndKeepsServing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, output := capturingDashboardLogger()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	jobProcessor := &panickingProcessor{}
	server := New(ctx, startedTracker(), backend, jobProcessor, logger, 1)

	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 5, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/5"}}

	// The queue slot must be released even though the job panicked, otherwise
	// the bounded queue would leak capacity permanently.
	deadline := time.Now().Add(3 * time.Second)
	for server.queued.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := server.queued.Load(); got != 0 {
		t.Fatalf("queued = %d after a panicking job, want 0", got)
	}

	// The worker goroutine must still be alive: send a second job and confirm
	// it is picked up rather than the server having died.
	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 6, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/6"}}
	deadline = time.Now().Add(3 * time.Second)
	for jobProcessor.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if jobProcessor.calls.Load() < 2 {
		t.Fatal("worker did not process a second job after the first panicked")
	}

	logged := output.String()
	if !strings.Contains(logged, "panicked") || !strings.Contains(logged, "link_id=5") {
		t.Fatalf("panic was not logged with its job: %s", logged)
	}
}

func TestDrainReturnsImmediatelyWhenNothingIsQueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)

	start := time.Now()
	server.Drain(time.Second)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Drain waited %v with an empty queue", elapsed)
	}
}

func TestDrainToleratesANonPositiveBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	server.queued.Store(1)

	// A shutdown path that already exhausted its deadline passes a non-positive
	// budget; it must not block.
	start := time.Now()
	server.Drain(0)
	server.Drain(-time.Second)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Drain blocked for %v with a non-positive budget", elapsed)
	}
}

func TestRecoveredPanicIsRecordedAsAJobFailure(t *testing.T) {
	// The tracker must reflect the failure so /status and the backstage page
	// surface it instead of the service looking healthy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger, _ := capturingDashboardLogger()
	tracker := startedTracker()
	server := New(ctx, tracker, &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}, &panickingProcessor{}, logger, 1)

	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 8, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/8"}}

	deadline := time.Now().Add(3 * time.Second)
	for server.queued.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := tracker.Snapshot()
	if snapshot.LastStats == nil || snapshot.LastStats.Failed != 1 {
		t.Fatalf("LastStats = %+v, want one recorded failure", snapshot.LastStats)
	}
	if snapshot.LastError == "" {
		t.Fatal("LastError is empty after a recovered panic")
	}
}

// TestDashboardStillServesAfterAManualJobPanics asserts the outcome that makes
// recovery worthwhile: the process keeps answering HTTP instead of dying.
func TestDashboardStillServesAfterAManualJobPanics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		jobs:      map[int64]*cairn.Job{1: {ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "t", LeaseUntil: "u"}},
		claimErrs: map[int64]error{},
	}
	server := New(ctx, startedTracker(), backend, &panickingProcessor{}, testLogger(), 1)

	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 1, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/1"}}
	deadline := time.Now().Add(3 * time.Second)
	for server.queued.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard stopped serving after a panic: status %d", response.Code)
	}
}
