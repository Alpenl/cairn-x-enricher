package localintegration

import (
	"context"
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
}
