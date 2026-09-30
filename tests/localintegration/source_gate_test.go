package localintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type localTransientSourceReader struct{ localSourceReader }
type localContractSourceReader struct{ localSourceReader }

func (r *localContractSourceReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	return enrich.Source{}, enrich.Classified(errors.New("fixture source contract rejected"),
		enrich.ErrorClassContract)
}

func (r *localTransientSourceReader) FetchSource(ctx context.Context, input enrich.Input) (enrich.Source, error) {
	r.fetches++
	if err := r.reserve(ctx, input, "fetch", false); err != nil {
		return enrich.Source{}, err
	}
	key := sha256.Sum256([]byte(input.LeaseToken + "fetch" + input.URL))
	if err := r.queue.SettleProviderAttempt(ctx, enrich.ProviderSettlement{
		OperationKey: hex.EncodeToString(key[:]), HTTPStatus: 529,
	}); err != nil {
		return enrich.Source{}, err
	}
	return enrich.Source{}, enrich.ClassifyModelError(&enrich.ModelHTTPError{
		StatusCode: 529, Type: "overloaded",
	})
}

func TestLocalWorkerSourceGateKeepsReadingAvailable(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	firstID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	first, err := queue.Claim(ctx)
	if err != nil || first == nil || first.ID != firstID {
		t.Fatalf("first source claim = %+v, %v", first, err)
	}
	reader := &localTransientSourceReader{localSourceReader{queue: queue}}
	worker := processor.NewStaged(queue, reader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(10 * time.Second)
	if err := worker.Process(ctx, first); !enrich.IsRetryable(err) || reader.fetches != 1 {
		t.Fatalf("source fixture did not open a transient gate: fetches=%d err=%v", reader.fetches, err)
	}
	waitingID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	readingID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	detail, err := queue.GetBookmark(ctx, readingID)
	if err != nil || detail.CacheIdentity == nil {
		t.Fatalf("reading revision = %+v, %v", detail.CacheIdentity, err)
	}
	if _, err := queue.SaveManualSource(ctx, readingID, fmt.Sprintf("source-gate-%d", readingID),
		detail.CacheIdentity.ContentRevision, "Source already stored before reading"); err != nil {
		t.Fatal(err)
	}
	reading, err := queue.Claim(ctx)
	if err != nil || reading == nil || reading.ID != readingID {
		t.Fatalf("source gate blocked independent reading: %+v, %v", reading, err)
	}
	healthy := &localSourceReader{queue: queue}
	readingWorker := processor.NewStaged(queue, healthy, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	readingWorker.SetPaidStageTimeout(10 * time.Second)
	if err := readingWorker.Process(ctx, reading); err != nil || healthy.fetches != 0 || healthy.transforms != 1 {
		t.Fatalf("stored-source reading = fetches=%d transforms=%d err=%v",
			healthy.fetches, healthy.transforms, err)
	}
	if next, err := queue.Claim(ctx); next != nil {
		t.Fatalf("source gate claimed waiting job: %+v", next)
	} else {
		var apiErr *cairn.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "component_paused" {
			t.Fatalf("source gate response = %v", err)
		}
	}
	waiting, err := queue.GetBookmark(ctx, waitingID)
	if err != nil || waiting.Attempts != 0 {
		t.Fatalf("source fault consumed a waiting attempt: %+v %v", waiting, err)
	}
}

func TestLocalWorkerInstanceStagePauseDoesNotBlockHealthyInstance(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	firstID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	first, err := queue.Claim(ctx)
	if err != nil || first == nil || first.ID != firstID {
		t.Fatalf("initial source claim = %+v, %v", first, err)
	}
	badReader := &localContractSourceReader{localSourceReader{queue: queue}}
	bad := processor.NewStaged(queue, badReader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	bad.SetPaidStageTimeout(10 * time.Second)
	if err := bad.Process(ctx, first); !enrich.PausesComponent(err) || badReader.fetches != 1 {
		t.Fatalf("contract fixture did not pause local source: fetches=%d err=%v", badReader.fetches, err)
	}
	if paused, _, _ := bad.SourceStagePaused("source"); !paused {
		t.Fatal("source stage is not paused after contract fault")
	}
	waitingID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	readingID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	detail, err := queue.GetBookmark(ctx, readingID)
	if err != nil || detail.CacheIdentity == nil {
		t.Fatalf("manual reading revision = %+v, %v", detail.CacheIdentity, err)
	}
	if _, err := queue.SaveManualSource(ctx, readingID, fmt.Sprintf("local-stage-%d", readingID),
		detail.CacheIdentity.ContentRevision, "Already stored source for reading"); err != nil {
		t.Fatal(err)
	}
	stats, err := bad.RunSources(ctx, 3)
	if err != nil || stats.Completed != 1 || stats.Claimed != 1 || badReader.fetches != 1 ||
		badReader.transforms != 1 {
		t.Fatalf("paused source blocked reading: %+v fetches=%d transforms=%d err=%v",
			stats, badReader.fetches, badReader.transforms, err)
	}
	waiting, err := queue.GetBookmark(ctx, waitingID)
	if err != nil || waiting.Attempts != 0 {
		t.Fatalf("bad instance consumed waiting source: %+v %v", waiting, err)
	}
	goodReader := &localSourceReader{queue: queue}
	good := processor.NewStaged(queue, goodReader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	good.SetPaidStageTimeout(10 * time.Second)
	stats, err = good.RunSources(ctx, 1)
	if err != nil || stats.Completed != 1 || goodReader.fetches != 1 || goodReader.transforms != 1 {
		t.Fatalf("healthy instance could not fetch: %+v fetches=%d transforms=%d err=%v",
			stats, goodReader.fetches, goodReader.transforms, err)
	}
	if paused, _, _ := bad.SourceStagePaused("source"); !paused {
		t.Fatal("another instance's success cleared a local configuration fault")
	}
}
