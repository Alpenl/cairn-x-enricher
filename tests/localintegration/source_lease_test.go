package localintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type localSourceReader struct {
	fetches, transforms int
	queue               *cairn.Client
}

type localFailedSourceReader struct{ localSourceReader }
type localFailedReadingReader struct{ localSourceReader }

type workerRequestRecorder struct {
	mu     sync.Mutex
	next   http.RoundTripper
	routes []string
}

func (r *workerRequestRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.routes = append(r.routes, request.Method+" "+request.URL.Path)
	r.mu.Unlock()
	return r.next.RoundTrip(request)
}

func (r *workerRequestRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.routes...)
}

func countRoute(routes []string, route string) int {
	n := 0
	for _, observed := range routes {
		if observed == route {
			n++
		}
	}
	return n
}

func (r *localFailedSourceReader) Transform(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	r.transforms++
	if err := r.reserve(ctx, input, "reading", false); err != nil {
		return enrich.Result{}, err
	}
	return enrich.Result{}, &enrich.ModelHTTPError{StatusCode: http.StatusBadGateway, Type: "upstream_error"}
}

func (r *localFailedReadingReader) Transform(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	r.transforms++
	if err := r.reserve(ctx, input, "reading", true); err != nil {
		return enrich.Result{}, err
	}
	return enrich.Result{}, errors.New("fixture reading validation failed")
}

func (r *localSourceReader) reserve(ctx context.Context, input enrich.Input, stage string, known bool) error {
	key := sha256.Sum256([]byte(input.LeaseToken + stage + input.URL))
	hash := sha256.Sum256([]byte("fixture-" + stage))
	operation := hex.EncodeToString(key[:])
	variant := "fetch_thread"
	if stage == "reading" {
		variant = "reading"
	}
	granted, err := r.queue.ReserveProviderAttempt(ctx, enrich.ProviderAttempt{
		OperationKey: operation, RequestHash: hex.EncodeToString(hash[:]), Model: "manual",
		Stage: stage, Variant: variant, AttemptNumber: 1,
		LinkID: input.ID, LeaseToken: input.LeaseToken, ContentRevision: input.ContentRevision,
		MinRemainingMS: input.MinRemainingMS,
	})
	if err != nil {
		return err
	}
	if !granted {
		return errors.New("fixture provider attempt was not granted")
	}
	if !known {
		return nil
	}
	return r.queue.SettleProviderAttempt(ctx, enrich.ProviderSettlement{OperationKey: operation, HTTPStatus: 200})
}

func (r *localSourceReader) FetchSource(ctx context.Context, input enrich.Input) (enrich.Source, error) {
	r.fetches++
	if err := r.reserve(ctx, input, "fetch", true); err != nil {
		return enrich.Source{}, err
	}
	return enrich.Source{OriginalText: "Fixture original text", Model: "manual",
		RelatedLinks: []string{}, ImageURLs: []string{}}, nil
}

func (r *localSourceReader) Transform(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	r.transforms++
	if err := r.reserve(ctx, input, "reading", true); err != nil {
		return enrich.Result{}, err
	}
	return enrich.Result{OriginalText: input.SourceText, OriginalLanguage: "en",
		AITitle: "Lease admission fixture", TranslatedText: "租约准入测试", Summary: "Fixture reading aid",
		Model: "manual"}, nil
}

func TestLocalWorkerSourceLeaseAdmission(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recorder := &workerRequestRecorder{next: http.DefaultTransport}
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second, Transport: recorder})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatalf("source lease handshake: %v", err)
	}
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("source claim = %+v, %v", job, err)
	}
	reader := &localSourceReader{queue: queue}
	worker := processor.NewStaged(queue, reader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(10 * time.Second)
	beforeProcess := len(recorder.snapshot())
	if err := worker.Process(ctx, job); err != nil {
		t.Fatalf("source processing with reading admission: %v", err)
	}
	processRoutes := recorder.snapshot()[beforeProcess:]
	// The old seven-call estimate predates source-lease admission and the
	// per-network-attempt ledger. Keep the current safe path's full HTTP
	// budget visible, with only the reading paid stage.
	if len(processRoutes) > 12 ||
		countRoute(processRoutes, "POST /api/enrichment/provider-attempts/reserve") != 1 ||
		countRoute(processRoutes, "POST /api/enrichment/provider-attempts/settle") != 1 ||
		countRoute(processRoutes, "POST /api/enrichment/jobs/"+strconv.FormatInt(id, 10)+"/lease-admit") != 1 {
		t.Fatalf("source request budget or paid-stage accounting changed: %v", processRoutes)
	}
	t.Logf("source Worker HTTP calls = %d (budget 12): %v", len(processRoutes), processRoutes)
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Status != "completed" ||
		detail.OriginalText != "Fixture original text" ||
		detail.Classification != nil ||
		reader.fetches != 0 || reader.transforms != 1 {
		t.Fatalf("source admission lifecycle = %+v, fetches=%d transforms=%d, err=%v",
			detail, reader.fetches, reader.transforms, err)
	}
	// The first completion was committed by Worker.Process. A lost HTTP
	// response must replay that receipt without running the model again.
	completion := cairn.Completion{LeaseToken: job.LeaseToken, AITitle: "Lease admission fixture",
		OriginalLanguage: "en", OriginalText: "Fixture original text",
		TranslatedText: "租约准入测试", Summary: "Fixture reading aid", Model: "manual",
		RelatedLinks: []string{}, Images: []cairn.ImageRef{}}
	if err := queue.Complete(ctx, id, completion); err != nil {
		t.Fatalf("exact completion replay: %v", err)
	}
	changed := completion
	changed.Summary = "different result"
	var conflict *cairn.APIError
	if err := queue.Complete(ctx, id, changed); !errors.As(err, &conflict) || conflict.Code != "operation_conflict" {
		t.Fatalf("different completion reused old receipt: %v", err)
	}
	if reader.fetches != 0 || reader.transforms != 1 {
		t.Fatalf("completion replay ran paid stages: fetches=%d transforms=%d", reader.fetches, reader.transforms)
	}
	// Personal changes must preserve the saved objective source and reading.
	// The source queue should stay empty, and no provider stage should run.
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		base+"/api/links/"+strconv.FormatInt(id, 10), strings.NewReader(`{"note":"private annotation changed"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("note-only update HTTP %d", response.StatusCode)
	}
	why, status := "my reason", "kept"
	if _, err := queue.UpdateCuration(ctx, id, cairn.CurationUpdate{Why: &why, Status: &status}); err != nil {
		t.Fatalf("why/status update: %v", err)
	}
	personal, err := queue.GetBookmark(ctx, id)
	if err != nil || personal.CacheIdentity == nil || detail.CacheIdentity == nil ||
		personal.CacheIdentity.ContentRevision != detail.CacheIdentity.ContentRevision ||
		personal.OriginalText != detail.OriginalText || personal.TranslatedText != detail.TranslatedText ||
		personal.Summary != detail.Summary || personal.Note != "private annotation changed" ||
		personal.Why != why || personal.CurationStatus != status || personal.Classification != nil {
		t.Fatalf("personal edit changed objective content: before=%+v after=%+v err=%v", detail, personal, err)
	}
	saved, err := queue.GetSource(ctx, id)
	if err != nil || saved == nil || saved.OriginalText != detail.OriginalText {
		t.Fatalf("personal edit removed source checkpoint: %+v %v", saved, err)
	}
	if claimed, err := queue.Claim(ctx); err != nil || claimed != nil || reader.fetches != 0 || reader.transforms != 1 {
		t.Fatalf("personal edit requeued paid source work: claim=%+v fetches=%d reading=%d err=%v",
			claimed, reader.fetches, reader.transforms, err)
	}

	failedID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	failedJob, err := queue.Claim(ctx)
	if err != nil || failedJob == nil || failedJob.ID != failedID {
		t.Fatalf("failed source claim = %+v, %v", failedJob, err)
	}
	failedReader := &localFailedSourceReader{localSourceReader{queue: queue}}
	failedWorker := processor.NewStaged(queue, failedReader, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	failedWorker.SetPaidStageTimeout(10 * time.Second)
	beforeFailedProcess := len(recorder.snapshot())
	if err := failedWorker.Process(ctx, failedJob); err == nil {
		t.Fatal("ambiguous provider response unexpectedly succeeded")
	} else {
		var modelErr *enrich.ModelHTTPError
		if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway {
			t.Fatalf("provider error = %v", err)
		}
	}
	failedRoutes := recorder.snapshot()[beforeFailedProcess:]
	failedJobBase := "POST /api/enrichment/jobs/" + strconv.FormatInt(failedID, 10)
	if len(failedRoutes) > 10 ||
		countRoute(failedRoutes, failedJobBase+"/lease-admit") != 1 ||
		countRoute(failedRoutes, "POST /api/enrichment/provider-attempts/reserve") != 1 ||
		countRoute(failedRoutes, "POST /api/enrichment/provider-attempts/settle") != 0 ||
		countRoute(failedRoutes, failedJobBase+"/fail") != 1 {
		t.Fatalf("unknown provider result request budget or ledger state changed: %v", failedRoutes)
	}
	t.Logf("unknown provider result Worker HTTP calls = %d (budget 5): %v", len(failedRoutes), failedRoutes)
	failedDetail, err := queue.GetBookmark(ctx, failedID)
	if err != nil || failedDetail.Status != "failed" || !failedDetail.PaidCallUnresolved ||
		failedDetail.PaidStage != "reading" || failedReader.transforms != 1 {
		t.Fatalf("ambiguous result guard = %+v, fetches=%d, err=%v", failedDetail, failedReader.fetches, err)
	}
	if claimed, err := queue.Claim(ctx); err != nil || claimed != nil {
		t.Fatalf("possibly paid call was claimed again: %+v, %v", claimed, err)
	}

	readingID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	readingJob, err := queue.Claim(ctx)
	if err != nil || readingJob == nil || readingJob.ID != readingID {
		t.Fatalf("reading-failure source claim: %+v %v", readingJob, err)
	}
	readingFailure := &localFailedReadingReader{localSourceReader{queue: queue}}
	readingWorker := processor.NewStaged(queue, readingFailure, nil, "", "",
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	readingWorker.SetPaidStageTimeout(10 * time.Second)
	if err := readingWorker.Process(ctx, readingJob); err == nil ||
		readingFailure.fetches != 0 || readingFailure.transforms != 1 {
		t.Fatalf("reading fixture did not fail after saving source: fetches=%d reading=%d err=%v",
			readingFailure.fetches, readingFailure.transforms, err)
	}
	retained, err := queue.GetSource(ctx, readingID)
	if err != nil || retained == nil || retained.OriginalText != "Fixture original text" {
		t.Fatalf("reading failure lost durable source: %+v %v", retained, err)
	}
	failedReading, err := queue.GetBookmark(ctx, readingID)
	if err != nil || failedReading.Status != "failed" || !failedReading.PaidCallUnresolved ||
		failedReading.PaidStage != "reading" || failedReading.OriginalText != retained.OriginalText {
		t.Fatalf("reading failure lost source or paid uncertainty: %+v %v", failedReading, err)
	}
	if retryJob, err := queue.ClaimByID(ctx, readingID); retryJob != nil || err == nil {
		t.Fatalf("unresolved paid reading was reclaimed: %+v %v", retryJob, err)
	} else {
		var apiErr *cairn.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "job_busy" {
			t.Fatalf("reading retry failed for the wrong reason: %v", err)
		}
	}
	t.Log("personal edits kept one source/reading attempt and no new source lease; failed reading kept the source and blocked a duplicate paid attempt")
}
