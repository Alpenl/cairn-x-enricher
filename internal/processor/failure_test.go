package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestBoundedErrorTruncatesOnRuneBoundaries(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ascii", errors.New(strings.Repeat("a", maxFailureMessageBytes*2))},
		// A cut through a multi-byte rune would produce invalid UTF-8, which
		// the Worker would then store and later fail to render.
		{"han", errors.New(strings.Repeat("中", maxFailureMessageBytes))},
		{"mixed", errors.New(strings.Repeat("a中", maxFailureMessageBytes))},
		{"emoji", errors.New(strings.Repeat("🙂", maxFailureMessageBytes))},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := boundedError(testCase.err)
			if len(got) > maxFailureMessageBytes {
				t.Fatalf("len(boundedError) = %d, want <= %d", len(got), maxFailureMessageBytes)
			}
			if !utf8.ValidString(got) {
				t.Fatal("boundedError produced invalid UTF-8")
			}
		})
	}
}

func TestBoundedErrorKeepsShortMessagesIntact(t *testing.T) {
	if got := boundedError(errors.New("short failure")); got != "short failure" {
		t.Fatalf("boundedError() = %q", got)
	}
}

func TestReportFailureRecordsTheBoundedMessage(t *testing.T) {
	queue := newFakeQueue()
	worker := New(queue, fakeEnricher{}, discardLogger(), 1)
	job := &cairn.Job{ID: 5, URL: "https://x.com/a/status/5", Attempt: 1, LeaseToken: "lease-5"}

	want := errors.New("model endpoint exploded")
	err := worker.reportFailure(context.Background(), worker.logger, job, want)
	if !errors.Is(err, want) {
		t.Fatalf("reportFailure() error = %v, want %v", err, want)
	}
	if got := queue.failures[5]; got != want.Error() {
		t.Fatalf("recorded failure = %q, want %q", got, want.Error())
	}
}

func TestReportFailureDoesNotReportInterruptedWork(t *testing.T) {
	// A cancelled attempt must not consume a retry: the lease will expire and
	// the job will be picked up again by design.
	queue := newFakeQueue()
	worker := New(queue, fakeEnricher{}, discardLogger(), 1)
	job := &cairn.Job{ID: 6, URL: "https://x.com/a/status/6", Attempt: 1, LeaseToken: "lease-6"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := worker.reportFailure(ctx, worker.logger, job, errors.New("upstream closed"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reportFailure() error = %v, want context.Canceled", err)
	}
	if _, recorded := queue.failures[6]; recorded {
		t.Fatal("an interrupted attempt was reported as a failure")
	}
}

func TestReportFailureSurfacesReportingErrors(t *testing.T) {
	queue := newFakeQueue()
	queue.failErr = errors.New("worker unavailable")
	worker := New(queue, fakeEnricher{}, discardLogger(), 1)
	job := &cairn.Job{ID: 7, URL: "https://x.com/a/status/7", Attempt: 1, LeaseToken: "lease-7"}

	err := worker.reportFailure(context.Background(), worker.logger, job, errors.New("model failure"))
	if err == nil || !strings.Contains(err.Error(), "report enrichment failure") {
		t.Fatalf("reportFailure() error = %v, want a reporting error", err)
	}
}
