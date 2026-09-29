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
