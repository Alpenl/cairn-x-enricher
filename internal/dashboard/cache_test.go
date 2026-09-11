package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestTaxonomyCacheAvoidsRepeatedUpstreamReads(t *testing.T) {
	backend := &fakeBackend{}
	cache := newTaxonomyCache(backend)

	for range 5 {
		catalog, err := cache.Catalog(context.Background())
		if err != nil {
			t.Fatalf("Catalog() error = %v", err)
		}
		if catalog.Version != "test-v1" {
			t.Fatalf("Catalog() version = %q", catalog.Version)
		}
	}
	if backend.taxonomyCalls != 1 {
		t.Fatalf("taxonomyCalls = %d, want 1", backend.taxonomyCalls)
	}
}

func TestTaxonomyCacheRefreshesAfterTTL(t *testing.T) {
	backend := &fakeBackend{}
	cache := newTaxonomyCache(backend)
	now := time.Now()
	cache.now = func() time.Time { return now }

	if _, err := cache.Catalog(context.Background()); err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	now = now.Add(taxonomyCacheTTL + time.Second)
	if _, err := cache.Catalog(context.Background()); err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if backend.taxonomyCalls != 2 {
		t.Fatalf("taxonomyCalls = %d, want 2", backend.taxonomyCalls)
	}
}

type failingTaxonomy struct {
	inner taxonomySource
	fail  atomic.Bool
}

func (f *failingTaxonomy) GetTaxonomy(ctx context.Context) (taxonomy.Catalog, error) {
	if f.fail.Load() {
		return taxonomy.Catalog{}, context.DeadlineExceeded
	}
	return f.inner.GetTaxonomy(ctx)
}

func TestTaxonomyCacheServesLastGoodCatalogDuringOutage(t *testing.T) {
	upstream := &failingTaxonomy{inner: &fakeBackend{}}
	cache := newTaxonomyCache(upstream)
	if _, err := cache.Catalog(context.Background()); err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	upstream.fail.Store(true)
	cache.Invalidate()

	catalog, err := cache.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v, want last known good catalog", err)
	}
	if catalog.Version != "test-v1" {
		t.Fatalf("Catalog() version = %q", catalog.Version)
	}
}

func TestTaxonomyCacheReportsFailureWithoutAnyCatalog(t *testing.T) {
	upstream := &failingTaxonomy{inner: &fakeBackend{}}
	upstream.fail.Store(true)
	if _, err := newTaxonomyCache(upstream).Catalog(context.Background()); err == nil {
		t.Fatal("Catalog() error = nil, want the upstream failure")
	}
}

func TestTaxonomyEndpointUsesTheCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	handler := server.Handler()

	for range 4 {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/taxonomy", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/taxonomy status = %d", response.Code)
		}
	}
	if backend.taxonomyCalls != 1 {
		t.Fatalf("taxonomyCalls = %d, want 1 for four requests", backend.taxonomyCalls)
	}
}

func TestBackstageSummaryIsCachedBetweenPolls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	handler := server.Handler()

	for range 3 {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/backstage", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/backstage status = %d", response.Code)
		}
	}
	// Two attention statuses, fetched once. The previous implementation also
	// made an extra unfiltered call and repeated all three on every poll.
	if backend.listCalls != len(backstageAttentionStatuses) {
		t.Fatalf("listCalls = %d, want %d", backend.listCalls, len(backstageAttentionStatuses))
	}
}

func TestBackstageSummaryRefreshesAfterTTL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	handler := server.Handler()

	fetch := func() {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/backstage", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/backstage status = %d", response.Code)
		}
	}
	fetch()
	// Expire the cache by rewinding the recorded timestamp rather than sleeping.
	server.summaryCachedAt = time.Now().Add(-backstageSummaryTTL - time.Second)
	fetch()
	if backend.listCalls != 2*len(backstageAttentionStatuses) {
		t.Fatalf("listCalls = %d, want %d", backend.listCalls, 2*len(backstageAttentionStatuses))
	}
}

func TestImageProxyRejectsTruncatedUpstreamBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{imageBody: "short", imageLength: 4096}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/images/enrichment/1/"+testImageKey, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.Len() != len("short") {
		t.Fatalf("body length = %d", response.Body.Len())
	}
}

func TestManualQueueCapacityUsesAtomicCounter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processing := make(chan int64, 1)
	backend := &fakeBackend{
		jobs:      map[int64]*cairn.Job{},
		claimErrs: map[int64]error{},
	}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: processing}, testLogger(), 1)

	if got := server.queued.Load(); got != 0 {
		t.Fatalf("initial queued = %d", got)
	}
	// Simulate a saturated queue without starting real work.
	server.queued.Store(int64(cap(server.jobs)))

	body := `{"ids":[1]}`
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/bookmarks/process", stringReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the queue is saturated", response.Code)
	}
}

func TestDrainWaitsForQueuedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)

	done := make(chan struct{})
	server.queued.Store(1)
	go func() {
		defer close(done)
		time.Sleep(30 * time.Millisecond)
		server.queued.Add(-1)
	}()

	start := time.Now()
	server.Drain(2 * time.Second)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("Drain returned after %v, before queued work finished", elapsed)
	}
	<-done
}

func TestDrainTimesOutRatherThanBlockingForever(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	server.queued.Store(1)

	start := time.Now()
	server.Drain(50 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Drain blocked for %v", elapsed)
	}
}

func TestWorkerReleasesQueueSlotAfterProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processed := make(chan int64, 1)
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{processed: processed}, testLogger(), 1)

	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 3, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/3"}}
	select {
	case id := <-processed:
		if id != 3 {
			t.Fatalf("processed id = %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not process the job")
	}
	deadline := time.Now().Add(2 * time.Second)
	for server.queued.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := server.queued.Load(); got != 0 {
		t.Fatalf("queued = %d after processing, want 0", got)
	}
}

func TestWriteBackendErrorMapsUpstreamCodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)

	cases := []struct {
		err  error
		want int
	}{
		{&cairn.APIError{StatusCode: http.StatusNotFound, Code: "not_found"}, http.StatusNotFound},
		{&cairn.APIError{StatusCode: http.StatusConflict, Code: "job_busy"}, http.StatusConflict},
		{&cairn.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_status"}, http.StatusBadRequest},
		{&cairn.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_query"}, http.StatusBadRequest},
		{&cairn.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_curation"}, http.StatusBadRequest},
		{&cairn.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_limit"}, http.StatusBadRequest},
		{&cairn.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_before_id"}, http.StatusBadRequest},
		{&cairn.APIError{StatusCode: http.StatusInternalServerError, Code: "boom"}, http.StatusBadGateway},
		{errPlain, http.StatusBadGateway},
	}
	for _, testCase := range cases {
		response := httptest.NewRecorder()
		server.writeBackendError(response, "test", 0, testCase.err)
		if response.Code != testCase.want {
			t.Errorf("writeBackendError(%v) status = %d, want %d", testCase.err, response.Code, testCase.want)
		}
		var payload map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if payload["error"] == "" {
			t.Errorf("writeBackendError(%v) returned no error code", testCase.err)
		}
	}
}

var errPlain = &plainError{}

type plainError struct{}

func (*plainError) Error() string { return "plain failure" }

// testImageKey is a well-formed R2 enrichment key matching one bookmark ID.
const testImageKey = "0000000000000000000000000000000000000000000000000000000000000000.jpg"

func stringReader(value string) *strings.Reader { return strings.NewReader(value) }
