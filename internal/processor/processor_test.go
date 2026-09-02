package processor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

type fakeQueue struct {
	mu          sync.Mutex
	jobs        []*cairn.Job
	claimErr    error
	completions map[int64]cairn.Completion
	failures    map[int64]string
}

func (q *fakeQueue) Claim(context.Context) (*cairn.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.jobs) == 0 {
		return nil, nil
	}
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, nil
}

func (q *fakeQueue) Complete(_ context.Context, id int64, completion cairn.Completion) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completions[id] = completion
	return nil
}

func (q *fakeQueue) Fail(_ context.Context, id int64, _ string, message string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failures[id] = message
	return nil
}

type fakeEnricher struct {
	failID int64
}

func (e fakeEnricher) Enrich(_ context.Context, input enrich.Input) (enrich.Result, error) {
	if input.ID == e.failID {
		return enrich.Result{}, errors.New("model failure")
	}
	return enrich.Result{
		OriginalText: "source",
		Summary:      "summary",
		RelatedLinks: []string{"https://example.com/source"},
		Model:        "grok-test",
	}, nil
}

func TestRunCompletesClaimedJobs(t *testing.T) {
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease-1"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "lease-2"},
	)
	processor := New(queue, fakeEnricher{}, discardLogger(), 2)

	stats, err := processor.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats.Claimed != 2 || stats.Completed != 2 || stats.Failed != 0 {
		t.Fatalf("Run() stats = %+v", stats)
	}
	if len(queue.completions) != 2 || queue.completions[1].LeaseToken != "lease-1" {
		t.Fatalf("completions = %#v", queue.completions)
	}
}

func TestRunReportsModelFailureAndStopsThatWorker(t *testing.T) {
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease-1"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "lease-2"},
	)
	processor := New(queue, fakeEnricher{failID: 1}, discardLogger(), 1)

	stats, err := processor.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats.Claimed != 1 || stats.Completed != 0 || stats.Failed != 1 {
		t.Fatalf("Run() stats = %+v", stats)
	}
	if queue.failures[1] != "run Eino enrichment workflow: model failure" && queue.failures[1] != "model failure" {
		t.Fatalf("failure = %q", queue.failures[1])
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("remaining jobs = %d", len(queue.jobs))
	}
}

func TestRunReturnsClaimError(t *testing.T) {
	want := errors.New("backend unavailable")
	queue := newFakeQueue()
	queue.claimErr = want
	processor := New(queue, fakeEnricher{}, discardLogger(), 1)

	_, err := processor.Run(context.Background(), 1)
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, want)
	}
}

func newFakeQueue(jobs ...*cairn.Job) *fakeQueue {
	return &fakeQueue{
		jobs:        jobs,
		completions: make(map[int64]cairn.Completion),
		failures:    make(map[int64]string),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
