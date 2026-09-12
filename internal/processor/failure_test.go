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
	err := worker.reportFailure(context.Background(), worker.logger, job, failurePathSearch, want)
	if !errors.Is(err, want) {
		t.Fatalf("reportFailure() error = %v, want %v", err, want)
	}
	if got, wantMsg := queue.failures[5], "[search] "+want.Error(); got != wantMsg {
		t.Fatalf("recorded failure = %q, want %q", got, wantMsg)
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
	err := worker.reportFailure(ctx, worker.logger, job, failurePathSearch, errors.New("upstream closed"))
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

	err := worker.reportFailure(context.Background(), worker.logger, job, failurePathSearch, errors.New("model failure"))
	if err == nil || !strings.Contains(err.Error(), "report enrichment failure") {
		t.Fatalf("reportFailure() error = %v, want a reporting error", err)
	}
}

// TestFailureMessageNamesTheInputPath guards the operator's ability to tell a
// retrieval failure from a recovery failure.
//
// Only a search failure means the bookmark's content is still missing. A
// recovery failure means the already-stored text could not be reformatted,
// which is a different problem: re-running the same job will not help, because
// the recovery path never re-fetches the post. Without the label both cases
// look identical in the stored failure message.
func TestFailureMessageNamesTheInputPath(t *testing.T) {
	cases := []struct {
		path failurePathLabel
		want string
	}{
		{failurePathSearch, "[search] model endpoint exploded"},
		{failurePathRecovered, "[recovered_source] model endpoint exploded"},
	}
	for _, tc := range cases {
		queue := newFakeQueue()
		worker := New(queue, fakeEnricher{}, discardLogger(), 1)
		job := &cairn.Job{ID: tc.path.ordinal(), URL: "https://x.com/a/status/1", Attempt: 2, LeaseToken: "lease"}

		_ = worker.reportFailure(context.Background(), worker.logger, job, tc.path, errors.New("model endpoint exploded"))
		if got := queue.failures[job.ID]; got != tc.want {
			t.Errorf("path %q: recorded failure = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestFailurePathPrefixSurvivesTruncation keeps the label useful after the
// Worker truncates the stored message, which is why it is a prefix.
func TestFailurePathPrefixSurvivesTruncation(t *testing.T) {
	long := strings.Repeat("cause ", 300)
	message := prefixFailurePath(failurePathRecovered, long)
	const workerLimit = 70
	truncated := message
	if len(truncated) > workerLimit {
		truncated = truncated[:workerLimit]
	}
	if !strings.HasPrefix(truncated, "[recovered_source]") {
		t.Fatalf("truncated message = %q, want it to keep the path label", truncated)
	}
}

// ordinal gives each path a distinct job ID so the table test can key results.
func (p failurePathLabel) ordinal() int64 {
	if p == failurePathRecovered {
		return 2
	}
	return 1
}
