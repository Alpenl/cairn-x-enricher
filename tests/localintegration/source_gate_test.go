package localintegration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type localContractSourceReader struct{ localSourceReader }

func (r *localContractSourceReader) Transform(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	r.transforms++
	if err := r.reserve(ctx, input, "reading", true); err != nil {
		return enrich.Result{}, err
	}
	return enrich.Result{}, enrich.Classified(errors.New("fixture reading schema fault"), enrich.ErrorClassContract)
}
func TestLocalWorkerSourceGateKeepsReadingAvailable(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	waitingID := createURLOnlyLink(t, base, "app")
	localFixtureSQL(t, "UPDATE enrichment_component_gates SET state='open',retry_at='9999-01-01' WHERE component='source'")
	readingID := createLink(t, base, "app")
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != readingID {
		t.Fatalf("retired source gate blocked reading: %+v %v", job, err)
	}
	reader := &localSourceReader{queue: queue}
	worker := processor.NewStaged(queue, reader, nil, "", "", slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(10 * time.Second)
	if err := worker.Process(ctx, job); err != nil || reader.fetches != 0 || reader.transforms != 1 {
		t.Fatalf("reading invoked retrieval: fetch=%d reading=%d %v", reader.fetches, reader.transforms, err)
	}
	if next, err := queue.Claim(ctx); err != nil || next != nil {
		t.Fatalf("URL-only bookmark claimed: %+v %v", next, err)
	}
	waiting, err := queue.GetBookmark(ctx, waitingID)
	if err != nil || waiting.Attempts != 0 || waiting.Error != "capture_required" {
		t.Fatalf("waiting capture consumed an attempt: %+v %v", waiting, err)
	}
}
func TestLocalWorkerInstanceStagePauseDoesNotBlockHealthyInstance(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	id := createLink(t, base, "app")
	first, err := queue.Claim(ctx)
	if err != nil || first == nil || first.ID != id {
		t.Fatalf("claim: %+v %v", first, err)
	}
	badReader := &localContractSourceReader{localSourceReader{queue: queue}}
	bad := processor.NewStaged(queue, badReader, nil, "", "", slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	bad.SetPaidStageTimeout(10 * time.Second)
	if err := bad.Process(ctx, first); !enrich.PausesComponent(err) || badReader.transforms != 1 || badReader.fetches != 0 {
		t.Fatalf("reading fault did not pause locally: %v", err)
	}
	nextID := createLink(t, base, "app")
	stats, err := bad.RunSources(ctx, 1)
	if err != nil || stats.Claimed != 0 {
		t.Fatalf("faulted instance claimed reading: %+v %v", stats, err)
	}
	goodReader := &localSourceReader{queue: queue}
	good := processor.NewStaged(queue, goodReader, nil, "", "", slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	good.SetPaidStageTimeout(10 * time.Second)
	stats, err = good.RunSources(ctx, 1)
	if err != nil || stats.Completed != 1 || goodReader.transforms != 1 || goodReader.fetches != 0 {
		t.Fatalf("healthy instance blocked: %+v %v", stats, err)
	}
	detail, err := queue.GetBookmark(ctx, nextID)
	if err != nil || detail.Status != "completed" {
		t.Fatalf("next archived original not completed: %+v %v", detail, err)
	}
}
