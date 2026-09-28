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

type localSourceReader struct {
	fetches, transforms int
}

type localFailedSourceReader struct{ localSourceReader }

func (r *localFailedSourceReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	return enrich.Source{}, &enrich.ModelHTTPError{StatusCode: http.StatusBadGateway, Type: "upstream_error"}
}

func (r *localSourceReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	return enrich.Source{OriginalText: "Fixture source for lease admission", Model: "fixture",
		RelatedLinks: []string{}, ImageURLs: []string{}}, nil
}

func (r *localSourceReader) Transform(_ context.Context, input enrich.Input) (enrich.Result, error) {
	r.transforms++
	return enrich.Result{OriginalText: input.SourceText, OriginalLanguage: "en",
		AITitle: "Lease admission fixture", TranslatedText: "租约准入测试", Summary: "Fixture reading aid",
		Model: "fixture"}, nil
}

func TestLocalWorkerSourceLeaseAdmission(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatalf("source lease handshake: %v", err)
	}
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("source claim = %+v, %v", job, err)
	}
	reader := &localSourceReader{}
	worker := processor.NewStaged(queue, reader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(10 * time.Second)
	if err := worker.Process(ctx, job); err != nil {
		t.Fatalf("source processing with two paid-stage admissions: %v", err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Status != "completed" ||
		detail.OriginalText != "Fixture source for lease admission" ||
		reader.fetches != 1 || reader.transforms != 1 {
		t.Fatalf("source admission lifecycle = %+v, fetches=%d transforms=%d, err=%v",
			detail, reader.fetches, reader.transforms, err)
	}

	failedID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	failedJob, err := queue.Claim(ctx)
	if err != nil || failedJob == nil || failedJob.ID != failedID {
		t.Fatalf("failed source claim = %+v, %v", failedJob, err)
	}
	failedReader := &localFailedSourceReader{}
	failedWorker := processor.NewStaged(queue, failedReader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	failedWorker.SetPaidStageTimeout(10 * time.Second)
	if err := failedWorker.Process(ctx, failedJob); err == nil {
		t.Fatal("ambiguous provider response unexpectedly succeeded")
	} else {
		var modelErr *enrich.ModelHTTPError
		if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway {
			t.Fatalf("provider error = %v", err)
		}
	}
	failedDetail, err := queue.GetBookmark(ctx, failedID)
	if err != nil || failedDetail.Status != "failed" || !failedDetail.PaidCallUnresolved ||
		failedDetail.PaidStage != "fetch" || failedReader.fetches != 1 {
		t.Fatalf("ambiguous result guard = %+v, fetches=%d, err=%v", failedDetail, failedReader.fetches, err)
	}
	if claimed, err := queue.Claim(ctx); err != nil || claimed != nil {
		t.Fatalf("possibly paid call was claimed again: %+v, %v", claimed, err)
	}
}
