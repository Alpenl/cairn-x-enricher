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
	processing := make(chan int64, 1)
	server := New(ctx, startedTracker(), &fakeBackend{}, &slowProcessor{started: processing, hold: 40 * time.Millisecond}, testLogger(), 1)

	// Admit a real job so a real worker consumes it during the drain.
	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 1, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/1"}}
	select {
	case <-processing:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started the job")
	}

	start := time.Now()
	server.Drain(2 * time.Second)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("Drain returned after %v, before the in-flight job finished", elapsed)
	}
	if got := server.queued.Load(); got != 0 {
		t.Fatalf("queued = %d after Drain, want 0", got)
	}
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

func TestImageProxyNeverPersistsPrivateImages(t *testing.T) {
	for _, upstream := range []string{"", "public, max-age=604800, immutable", "private, max-age=86400", "no-store"} {
		t.Run(upstream, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend := &fakeBackend{imageBody: "jpeg-data", imageCacheControl: upstream, omitImageCacheControl: upstream == ""}
			server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/images/enrichment/1/"+testImageKey, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != "jpeg-data" {
				t.Fatalf("image = %d %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private, no-store", got)
			}
		})
	}
}

// slowProcessor blocks for a fixed period so a drain can observe in-flight work.
type slowProcessor struct {
	started chan int64
	hold    time.Duration
}

func (p *slowProcessor) Process(_ context.Context, job *cairn.Job) error {
	p.started <- job.ID
	time.Sleep(p.hold)
	return nil
}

func (p *slowProcessor) ProcessWithSource(ctx context.Context, job *cairn.Job, _ string) error {
	return p.Process(ctx, job)
}

func TestDrainRefusesNewWorkOnceItStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		jobs:      map[int64]*cairn.Job{1: {ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "t", LeaseUntil: "u"}},
		claimErrs: map[int64]error{},
	}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	server.Drain(0) // no queued work: only flips the draining flag

	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/bookmarks/process", strings.NewReader(`{"ids":[1]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while draining", response.Code)
	}
	if !strings.Contains(response.Body.String(), "shutting_down") {
		t.Fatalf("body = %s, want the shutting_down code", response.Body.String())
	}
}

func TestDrainStopsWorkersSoNoGoroutineOutlivesIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 2)
	server.Drain(0)

	// Workers must have exited: a send to the (unbuffered-consumer) channel
	// would otherwise be picked up after Drain returned.
	if !waitForWorkers(&server.workers, time.Second) {
		t.Fatal("worker goroutines outlived Drain")
	}
}

func TestDrainStopsWorkersEvenWithACancelledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := New(ctx, startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 3)
	server.Drain(0)
	if !waitForWorkers(&server.workers, time.Second) {
		t.Fatal("workers outlived Drain")
	}
}

// TestDrainConsumesJobsAdmittedBeforeShutdown is the regression test for the
// bug this change fixes.
//
// A worker blocked on an empty queue is the deterministic case: with `select`,
// a cancelled context is chosen eventually, but with the old shared context the
// worker exits before it ever takes the job that is admitted afterwards. The
// processor here blocks until released, so the job can only complete if a live
// worker picked it up during Drain.
func TestDrainConsumesJobsAdmittedBeforeShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	release := make(chan struct{})
	started := make(chan int64, 4)
	server := New(ctx, startedTracker(), &fakeBackend{}, &gatedProcessor{started: started, release: release}, testLogger(), 1)

	// Cancel the parent first, mirroring SIGTERM arriving before Drain runs.
	cancel()
	time.Sleep(50 * time.Millisecond)

	server.queued.Add(1)
	server.jobs <- manualJob{job: &cairn.Job{ID: 7, Attempt: 1, LeaseToken: "t", LeaseUntil: "u", URL: "https://x.com/a/status/7"}}

	drained := make(chan struct{})
	go func() { defer close(drained); server.Drain(5 * time.Second) }()

	select {
	case id := <-started:
		if id != 7 {
			t.Fatalf("started job %d, want 7", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the admitted job was never picked up during Drain")
	}
	close(release)
	<-drained
	if got := server.queued.Load(); got != 0 {
		t.Fatalf("queued = %d after Drain, want 0", got)
	}
}

// gatedProcessor blocks until released, so a job can only finish if a live
// worker actually picked it up.
type gatedProcessor struct {
	started chan int64
	release chan struct{}
}

func (p *gatedProcessor) Process(_ context.Context, job *cairn.Job) error {
	p.started <- job.ID
	<-p.release
	return nil
}

func (p *gatedProcessor) ProcessWithSource(ctx context.Context, job *cairn.Job, _ string) error {
	return p.Process(ctx, job)
}
