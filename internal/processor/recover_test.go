package processor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// panicEnricher panics for a specific job ID, simulating a bug in the
// enrichment path.
type panicEnricher struct {
	panicID int64
	seen    atomic.Int64
}

func (p *panicEnricher) Enrich(_ context.Context, input enrich.Input) (enrich.Result, error) {
	p.seen.Add(1)
	if input.ID == p.panicID {
		panic("simulated enrichment bug")
	}
	return enrich.Result{
		AITitle: "并发测试的中文标题内容", OriginalLanguage: "en", OriginalText: "s",
		TranslatedText: "译", Summary: "摘", Model: "m",
	}, nil
}

func capturingLogger() (*slog.Logger, *strings.Builder) {
	builder := &strings.Builder{}
	return slog.New(slog.NewTextHandler(builder, nil)), builder
}

func TestRecoverJobConvertsPanicIntoAnError(t *testing.T) {
	logger, output := capturingLogger()
	var err error
	func() {
		defer RecoverJob(logger, "manual enrichment", 42, &err)
		panic("boom")
	}()

	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("recovered error = %v, want the panic value", err)
	}
	logged := output.String()
	if !strings.Contains(logged, "link_id=42") {
		t.Errorf("log does not attribute the panic to the job: %s", logged)
	}
	if !strings.Contains(logged, "stack=") {
		t.Errorf("log does not include a stack trace: %s", logged)
	}
}

func TestRecoverJobLeavesAnExistingErrorAlone(t *testing.T) {
	logger, _ := capturingLogger()
	want := errors.New("original")
	err := want
	func() {
		defer RecoverJob(logger, "op", 1, &err)
		panic("ignored")
	}()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the pre-existing error preserved", err)
	}
}

func TestRecoverJobIsANoOpWithoutAPanic(t *testing.T) {
	logger, output := capturingLogger()
	err := error(nil)
	func() {
		defer RecoverJob(logger, "op", 7, &err)
	}()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if output.Len() != 0 {
		t.Fatalf("a clean return logged something: %s", output.String())
	}
}

func TestRecoverTaskToleratesANilJobID(t *testing.T) {
	logger, output := capturingLogger()
	func() {
		defer RecoverTask(logger, "scheduler")
		panic("loop bug")
	}()
	if !strings.Contains(output.String(), "scheduler panicked") {
		t.Fatalf("log = %s", output.String())
	}
}

func TestScheduledWorkerSurvivesAPanickingJob(t *testing.T) {
	// Without recovery this panic would terminate the test process, so simply
	// completing proves the batch worker is protected.
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "l1"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "l2"},
	)
	enricher := &panicEnricher{panicID: 1}
	worker := New(queue, enricher, discardLogger(), 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = worker.Run(context.Background(), 2)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a panicking job")
	}
	if enricher.seen.Load() == 0 {
		t.Fatal("the enricher never ran")
	}
}

func TestProcessorProcessPropagatesPanicToTheCaller(t *testing.T) {
	// Process is called from the dashboard worker, which owns its own recovery
	// and needs the failure reported as a job error rather than a crash.
	queue := newFakeQueue()
	worker := New(queue, &panicEnricher{panicID: 9}, discardLogger(), 1)
	job := &cairn.Job{ID: 9, URL: "https://x.com/a/status/9", Attempt: 1, LeaseToken: "l"}

	var err error
	func() {
		defer RecoverJob(discardLogger(), "manual enrichment", job.ID, &err)
		err = worker.Process(context.Background(), job)
	}()
	if err == nil {
		t.Fatal("err = nil, want the recovered panic")
	}
}

// TestPanickingJobSurfacesAsABatchError is the property that makes recovery
// useful: the process survives, and the operator still learns which batch
// failed and why. Recovering into a log line alone would hide the failure.
func TestPanickingJobSurfacesAsABatchError(t *testing.T) {
	queue := newFakeQueue(&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "l"})
	logger, output := capturingLogger()
	worker := New(queue, &panicEnricher{panicID: 1}, logger, 1)

	stats, err := worker.Run(context.Background(), 1)
	if err == nil {
		t.Fatalf("Run() error = nil, want the panic reported; stats=%+v", stats)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("Run() error = %v, want it to mention the panic", err)
	}
	if !strings.Contains(output.String(), "stack=") {
		t.Fatalf("no stack logged: %s", output.String())
	}
}
