package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type fakeBackend struct {
	mu                     sync.Mutex
	query                  cairn.BookmarkQuery
	page                   cairn.BookmarkPage
	pages                  map[string]cairn.BookmarkPage
	detail                 cairn.BookmarkDetail
	jobs                   map[int64]*cairn.Job
	claimErrs              map[int64]error
	enqueueErrs            map[int64]error
	queuedIDs              []int64
	queuedKeys             []string
	imageBody              string
	curation               cairn.CurationUpdate
	manualSourceText       string
	manualOperationKey     string
	manualExpectedRevision int64
	manualSaveErr          error

	// Counters let tests assert that handler-level caching actually removes
	// upstream round trips.
	listCalls     int
	taxonomyCalls int
	imageErr      error
	imageLength   int
	// omitImageCacheControl simulates a backend that sends no Cache-Control.
	omitImageCacheControl bool
	imageCacheControl     string
}

func TestBookmarkQueryOnlySkipsCountsWhenExplicitlyRequested(t *testing.T) {
	for path, want := range map[string]struct{ skip, valid bool }{
		"/api/bookmarks":                   {false, true},
		"/api/bookmarks?counts=1":          {false, true},
		"/api/bookmarks?counts=0":          {true, true},
		"/api/bookmarks?counts=2":          {false, false},
		"/api/bookmarks?counts=0&counts=1": {false, false},
	} {
		query, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if (err == nil) != want.valid || err == nil && query.SkipCounts != want.skip {
			t.Errorf("bookmarkQuery(%q) = %+v, %v", path, query, err)
		}
	}
}

func TestBookmarkPageOmitsCountsWhenSkipped(t *testing.T) {
	backend := &fakeBackend{page: cairn.BookmarkPage{Items: []cairn.Bookmark{}, Counts: cairn.BookmarkCounts{Total: 99}}}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks?counts=0", nil))
	var body map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("count-free list = %d %s", response.Code, response.Body.String())
	}
	if !backend.query.SkipCounts || body["counts"] != nil {
		t.Fatalf("count-free list kept counts: query %+v, body %s", backend.query, response.Body.String())
	}
}

func TestReadingRouteValidatesVersionBeforeCheckingOptionalBackend(t *testing.T) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for path, want := range map[string]int{
		"/api/bookmarks/7/reading?body_revision=x":  http.StatusBadRequest,
		"/api/bookmarks/7/reading?body_revision=01": http.StatusBadRequest,
		"/api/bookmarks/7/reading":                  http.StatusServiceUnavailable,
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("GET %s = %d, want %d", path, response.Code, want)
		}
	}
}

func (b *fakeBackend) ListBookmarks(_ context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listCalls++
	b.query = query
	if b.pages != nil {
		if page, ok := b.pages[query.Status]; ok {
			return page, nil
		}
	}
	return b.page, nil
}

func (b *fakeBackend) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return b.detail, nil
}

func (b *fakeBackend) GetTaxonomy(context.Context) (taxonomy.Catalog, error) {
	b.mu.Lock()
	b.taxonomyCalls++
	b.mu.Unlock()
	return taxonomy.Catalog{
		Version: "test-v1",
		Topics:  []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}},
		Forms:   []taxonomy.Term{{ID: "tool", Label: "工具", Active: true}},
		Uses:    []taxonomy.Term{{ID: "try", Label: "待试", Active: true}},
	}, nil
}

func (b *fakeBackend) UpdateCuration(_ context.Context, _ int64, update cairn.CurationUpdate) (cairn.BookmarkDetail, error) {
	b.curation = update
	return b.detail, nil
}

func (b *fakeBackend) GetImage(context.Context, string) (*http.Response, error) {
	if b.imageErr != nil {
		return nil, b.imageErr
	}
	header := http.Header{
		"Content-Type": []string{"image/jpeg"},
		"ETag":         []string{`"test-image"`},
	}
	switch {
	case b.imageCacheControl != "":
		header.Set("Cache-Control", b.imageCacheControl)
	case !b.omitImageCacheControl:
		header.Set("Cache-Control", "private, max-age=86400")
	}
	body := b.imageBody
	if b.imageLength > 0 {
		// Simulate an upstream that under-delivers relative to its declared
		// Content-Length, which must not be silently accepted.
		header.Set("Content-Length", strconv.Itoa(b.imageLength))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (b *fakeBackend) ClaimByID(_ context.Context, id int64) (*cairn.Job, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.claimErrs[id]; err != nil {
		return nil, err
	}
	return b.jobs[id], nil
}

func (b *fakeBackend) RequestEnrichment(_ context.Context, id int64, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.enqueueErrs[id]; err != nil {
		return err
	}
	b.queuedIDs = append(b.queuedIDs, id)
	b.queuedKeys = append(b.queuedKeys, key)
	return nil
}

func (b *fakeBackend) SaveManualSource(_ context.Context, id int64, key string, expected int64, source string) (cairn.ManualSourceResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.manualSourceText = source
	b.manualOperationKey = key
	b.manualExpectedRevision = expected
	if b.manualSaveErr != nil {
		return cairn.ManualSourceResult{}, b.manualSaveErr
	}
	return cairn.ManualSourceResult{ID: id, Status: "source_saved", ContentRevision: expected + 1}, nil
}

type fakeProcessor struct {
	processed chan int64
	sources   chan sourceProcess
}

type sourceProcess struct {
	ID         int64
	SourceText string
}

func (p *fakeProcessor) Process(_ context.Context, job *cairn.Job) error {
	p.processed <- job.ID
	return nil
}

func (p *fakeProcessor) ProcessWithSource(_ context.Context, job *cairn.Job, sourceText string) error {
	p.sources <- sourceProcess{ID: job.ID, SourceText: sourceText}
	return nil
}

func TestHandlerServesChineseDashboardAndBookmarkData(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		page: cairn.BookmarkPage{
			Items: []cairn.Bookmark{{
				ID: 7, URL: "https://x.com/example/status/7", Note: "测试收藏",
				CreatedAt: "2026-09-03T00:00:00Z", Status: "completed", Processable: true,
				AITitle: "人工智能生成的测试中文标题", OriginalLanguage: "en",
				OriginalText: "完整原文", TranslatedText: "完整简体中文译文", Summary: "测试总结",
				RelatedURLs: []string{},
			}},
			Counts: cairn.BookmarkCounts{Total: 1, Completed: 1},
		},
		detail: cairn.BookmarkDetail{
			Bookmark: cairn.Bookmark{
				ID: 7, URL: "https://x.com/example/status/7", Note: "测试收藏", Status: "completed",
				AITitle: "人工智能生成的测试中文标题", OriginalLanguage: "en",
				OriginalText: "完整原文", TranslatedText: "完整简体中文译文", RelatedURLs: []string{},
			},
		},
		jobs:      map[int64]*cairn.Job{},
		claimErrs: map[int64]error{},
		imageBody: "jpeg-data",
	}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: make(chan int64, 1), sources: make(chan sourceProcess, 1)}, testLogger(), 1)
	statusRequest := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/observability", nil)
	unavailable := httptest.NewRecorder()
	server.Handler().ServeHTTP(unavailable, statusRequest)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled observability status = %d", unavailable.Code)
	}
	server.SetObservabilityStatus(func() observability.Status {
		return observability.Status{Desired: observability.Policy{Version: 3, Logs: observability.LogOff}, EffectiveLogs: observability.LogOff, AppliedVersion: 3}
	})
	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, statusRequest)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"effective_logs":"off"`) || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read-only observability status = %d %q", status.Code, status.Body.String())
	}
	write := httptest.NewRecorder()
	server.Handler().ServeHTTP(write, httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/observability", strings.NewReader(`{"logs":"diagnostic"}`)))
	if write.Code != http.StatusMethodNotAllowed {
		t.Fatalf("LAN observability write = %d", write.Code)
	}

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "Cairn 收藏") {
		t.Fatalf("GET / = %d %q", root.Code, root.Body.String())
	}
	for _, label := range []string{"搜索收藏", "/assets/js/main.js", "/backstage", "整理状态", "收藏原因", "展开原文", "下一条"} {
		if !strings.Contains(root.Body.String(), label) {
			t.Errorf("GET / does not contain %q", label)
		}
	}
	if root.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("GET / is missing Content-Security-Policy")
	}
	if !strings.Contains(root.Header().Get("Content-Security-Policy"), "img-src 'self'") {
		t.Fatal("GET / Content-Security-Policy does not allow same-origin images")
	}
	if strings.Contains(root.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("GET / Content-Security-Policy still allows inline assets")
	}

	// Every page route serves the same application shell so deep links keep
	// working; the client router picks the view from the URL.
	for _, path := range []string{"/bookmarks/7", "/bookmarks/7?topics=llm&curation_status=all", "/backstage"} {
		page := httptest.NewRecorder()
		server.Handler().ServeHTTP(page, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		if page.Code != http.StatusOK || page.Body.String() != root.Body.String() {
			t.Fatalf("GET %s = %d, want the application shell", path, page.Code)
		}
		if page.Header().Get("Cache-Control") != "no-store" || page.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s headers = %v", path, page.Header())
		}
	}
	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, httptest.NewRequestWithContext(ctx, http.MethodGet, "/bookmarks/0", nil))
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("GET /bookmarks/0 = %d, want 404", invalid.Code)
	}

	script := httptest.NewRecorder()
	server.Handler().ServeHTTP(script, httptest.NewRequestWithContext(ctx, http.MethodGet, "/assets/js/main.js", nil))
	if script.Code != http.StatusOK || !strings.Contains(script.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("GET /assets/js/main.js = %d %q", script.Code, script.Header().Get("Content-Type"))
	}

	stylesheet := httptest.NewRecorder()
	server.Handler().ServeHTTP(stylesheet, httptest.NewRequestWithContext(ctx, http.MethodGet, "/assets/components.css", nil))
	if stylesheet.Code != http.StatusOK || !strings.Contains(stylesheet.Header().Get("Content-Type"), "text/css") || !strings.Contains(stylesheet.Body.String(), ".detail-pane") {
		t.Fatalf("GET /assets/app.css = %d %q", stylesheet.Code, stylesheet.Header().Get("Content-Type"))
	}

	list := httptest.NewRecorder()
	server.Handler().ServeHTTP(list, httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"/api/bookmarks?limit=10&status=completed&q=%E6%B5%8B%E8%AF%95",
		nil,
	))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "人工智能生成的测试中文标题") || !strings.Contains(list.Body.String(), "完整简体中文译文") || !strings.Contains(list.Body.String(), "测试收藏") {
		t.Fatalf("GET /api/bookmarks = %d %q", list.Code, list.Body.String())
	}
	if backend.query.Limit != 10 || backend.query.Status != "completed" || backend.query.Search != "测试" {
		t.Fatalf("bookmark query = %+v", backend.query)
	}

	detail := httptest.NewRecorder()
	server.Handler().ServeHTTP(detail, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/bookmarks/7", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "人工智能生成的测试中文标题") || !strings.Contains(detail.Body.String(), "完整简体中文译文") {
		t.Fatalf("GET /api/bookmarks/7 = %d %q", detail.Code, detail.Body.String())
	}

	image := httptest.NewRecorder()
	server.Handler().ServeHTTP(image, httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"/api/images/enrichment/7/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg",
		nil,
	))
	if image.Code != http.StatusOK || image.Body.String() != "jpeg-data" || image.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("GET /api/images/... = %d %q", image.Code, image.Body.String())
	}
}

func TestHandlerQueuesSelectedBookmarksAndReportsRejections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{enqueueErrs: map[int64]error{
		2: &cairn.APIError{StatusCode: http.StatusConflict, Code: "lease_conflict"},
	}}
	processed := make(chan int64, 1)
	tracker := startedTracker()
	server := New(ctx, tracker, backend, &fakeProcessor{processed: processed, sources: make(chan sourceProcess, 1)}, testLogger(), 1)
	wakeup := make(chan struct{}, 1)
	server.SetWakeup(wakeup)

	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/api/bookmarks/process",
		strings.NewReader(`{"ids":[1,2,1],"operation_keys":{"1":"op-1","2":"op-2"}}`),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("POST /api/bookmarks/process status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Accepted []int64 `json:"accepted"`
		Rejected []struct {
			ID    int64  `json:"id"`
			Error string `json:"error"`
		} `json:"rejected"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Accepted) != 1 || body.Accepted[0] != 1 {
		t.Fatalf("accepted = %v", body.Accepted)
	}
	if len(body.Rejected) != 1 || body.Rejected[0].ID != 2 || body.Rejected[0].Error != "lease_conflict" {
		t.Fatalf("rejected = %+v", body.Rejected)
	}
	if len(backend.queuedIDs) != 1 || backend.queuedIDs[0] != 1 || backend.queuedKeys[0] != "op-1" {
		t.Fatalf("durable requests = %v keys=%v", backend.queuedIDs, backend.queuedKeys)
	}
	select {
	case <-wakeup:
	default:
		t.Fatal("manual request did not wake scheduler")
	}
	select {
	case id := <-processed:
		t.Fatalf("old local worker ran %d", id)
	default:
	}
}

func TestHandlerReturnsManualQueueBackpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{enqueueErrs: map[int64]error{
		1: &cairn.APIError{StatusCode: http.StatusTooManyRequests, Code: "manual_queue_full", RetryAfter: "5"},
	}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: make(chan int64, 1),
		sources: make(chan sourceProcess, 1)}, testLogger(), 1)
	wakeup := make(chan struct{}, 1)
	server.SetWakeup(wakeup)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/bookmarks/process",
		strings.NewReader(`{"ids":[1]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "5" ||
		!strings.Contains(response.Body.String(), "manual_queue_full") {
		t.Fatalf("backpressure response = %d %s, retry-after=%q", response.Code,
			response.Body.String(), response.Header().Get("Retry-After"))
	}
	select {
	case <-wakeup:
		t.Fatal("rejected request woke scheduler")
	default:
	}
}

func TestBackstageSummaryMarksAttentionItemsAsActionable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	counts := cairn.BookmarkCounts{Total: 16, Exhausted: 3}
	backend := &fakeBackend{
		pages: map[string]cairn.BookmarkPage{
			"failed": {Counts: counts},
			"exhausted": {
				Items: []cairn.Bookmark{{
					ID:       2095768840005439592,
					URL:      "https://x.com/CarsonYangk8s/status/2095768840005439592",
					Status:   "exhausted",
					Attempts: 5,
					Error:    "run Eino enrichment workflow: model API returned HTTP 502: Upstream service temporarily unavailable",
				}},
				Counts: counts,
			},
		},
		jobs:      map[int64]*cairn.Job{},
		claimErrs: map[int64]error{},
	}
	tracker := startedTracker()
	tracker.Record(processor.Stats{}, nil)
	server := New(ctx, tracker, backend, &fakeProcessor{processed: make(chan int64, 1), sources: make(chan sourceProcess, 1)}, testLogger(), 1)

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/backstage", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/backstage = %d, body = %s", response.Code, response.Body.String())
	}

	var body struct {
		Title     string               `json:"title"`
		State     string               `json:"state"`
		Attention []cairn.Bookmark     `json:"attention"`
		Counts    cairn.BookmarkCounts `json:"counts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Title == "一切正常" || !strings.Contains(body.Title, "需要处理 3 条") {
		t.Fatalf("title = %q", body.Title)
	}
	for _, want := range []string{"最近一批没有领取到新任务", "还有 3 条需要人工处理"} {
		if !strings.Contains(body.State, want) {
			t.Fatalf("state = %q, want %q", body.State, want)
		}
	}
	if len(body.Attention) != 1 || body.Attention[0].Status != "exhausted" {
		t.Fatalf("attention = %+v", body.Attention)
	}
	if body.Counts.Total != 16 || body.Counts.Exhausted != 3 {
		t.Fatalf("counts = %+v", body.Counts)
	}
}

func TestHandlerSavesManualSourceBeforeAcceptance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		jobs: map[int64]*cairn.Job{
			20: {ID: 20, URL: "https://x.com/example/status/20", Attempt: 6, LeaseToken: "lease-20"},
		},
		claimErrs: map[int64]error{},
	}
	sources := make(chan sourceProcess, 1)
	server := New(ctx, startedTracker(), backend, &fakeProcessor{
		processed: make(chan int64, 1),
		sources:   sources,
	}, testLogger(), 1)
	wakeup := make(chan struct{}, 1)
	server.SetWakeup(wakeup)

	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/api/bookmarks/20/source",
		strings.NewReader(`{"original_text":" 人工粘贴原文 ","operation_key":"manual-20-1","expected_revision":7}`),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("POST /api/bookmarks/20/source status = %d, body = %s", response.Code, response.Body.String())
	}

	if backend.manualSourceText != " 人工粘贴原文 " || backend.manualOperationKey != "manual-20-1" || backend.manualExpectedRevision != 7 {
		t.Fatalf("manual source save = %q, %q, %d", backend.manualSourceText, backend.manualOperationKey, backend.manualExpectedRevision)
	}
	select {
	case <-wakeup:
	default:
		t.Fatal("scheduler was not notified")
	}
	select {
	case got := <-sources:
		t.Fatalf("source was processed through local queue: %+v", got)
	default:
	}
}

func TestHandlerDoesNotAcceptOrWakeWhenManualSourceSaveFails(t *testing.T) {
	for _, code := range []string{"input_changed", "lease_conflict"} {
		t.Run(code, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend := &fakeBackend{manualSaveErr: &cairn.APIError{StatusCode: http.StatusConflict, Code: code}}
			server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: make(chan int64, 1),
				sources: make(chan sourceProcess, 1)}, testLogger(), 1)
			wakeup := make(chan struct{}, 1)
			server.SetWakeup(wakeup)
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/bookmarks/20/source",
				strings.NewReader(`{"original_text":"post","operation_key":"manual-20-1","expected_revision":7}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), code) {
				t.Fatalf("failed save response = %d %s", response.Code, response.Body.String())
			}
			select {
			case <-wakeup:
				t.Fatal("failed save woke scheduler")
			default:
			}
		})
	}
}

func TestHandlerRejectsInvalidManagementRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: make(chan int64, 1), sources: make(chan sourceProcess, 1)}, testLogger(), 1)

	for _, test := range []struct {
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{http.MethodGet, "/api/bookmarks?limit=99", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/bookmarks?limit=40", "", "", http.StatusOK},
		{http.MethodGet, "/api/bookmarks/0", "", "", http.StatusBadRequest},
		{http.MethodPost, "/api/bookmarks/process", `{"ids":[]}`, "application/json", http.StatusBadRequest},
		{http.MethodPost, "/api/bookmarks/process", `{"ids":[1]}`, "text/plain", http.StatusBadRequest},
		{http.MethodPost, "/api/bookmarks/1/source", `{"original_text":""}`, "application/json", http.StatusConflict},
		{http.MethodPost, "/api/bookmarks/1/source", `{"original_text":"text"}`, "text/plain", http.StatusBadRequest},
		{http.MethodGet, "/missing", "", "", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(ctx, test.method, test.path, strings.NewReader(test.body))
		if test.contentType != "" {
			request.Header.Set("Content-Type", test.contentType)
		}
		server.Handler().ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

// startedTracker returns a tracker that has completed startup, which is the
// state every dashboard handler is exercised in.
func startedTracker() *health.Tracker {
	tracker := health.NewTracker()
	tracker.MarkStarted()
	return tracker
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestCurationValidatesEditsAndForwardsFacets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{detail: cairn.BookmarkDetail{Bookmark: cairn.Bookmark{ID: 7}}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"why":"用于评审","curation_status":"kept","classification":{"topics":["llm"],"form":"tool","use":"try"}}`, 200},
		{`{"classification":null}`, 200},
		{`{"classification":{"topics":["llm"],"form":"tool","use":"try"},"expected_revision":0,"operation_key":"confirm-7"}`, 200},
		{`{"classification":null,"expected_revision":0,"operation_key":"bad-reset"}`, 400},
		{`{"classification":{"topics":["llm"],"form":"tool","use":"try"},"expected_revision":0}`, 400},
		{`{"classification":{"topics":["llm"],"form":"tool","use":"try"},"operation_key":""}`, 400},
		{`{"why":"ok","classification":{"topics":["llm"],"form":"tool","use":"try"},"expected_revision":0,"operation_key":"bad-mixed"}`, 400},
		{`{"classification":null}`, 200},
		{`{"classification":{"topics":["invented"],"form":"tool","use":"try"}}`, 400},
		{`{"classification":{"topics":["llm","llm"],"form":"tool","use":"try"}}`, 400},
		{`{"classification":{"topics":[],"form":"tool","use":"try","entities":[]}}`, 400},
		{`{"curation_status":"completed"}`, 400},
		{`{"why":"` + strings.Repeat("字", 201) + `"}`, 400},
		{`{"why":"ok"} {}`, 400},
		{`{}`, 400},
	} {
		request := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/api/bookmarks/7/curation", strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		writer := httptest.NewRecorder()
		server.Handler().ServeHTTP(writer, request)
		if writer.Code != test.status {
			t.Fatalf("curation %s = %d: %s", test.body, writer.Code, writer.Body.String())
		}
	}
	if string(backend.curation.Classification) != "null" {
		t.Fatalf("reset classification was not preserved: %s", backend.curation.Classification)
	}
	guarded := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/api/bookmarks/7/curation",
		strings.NewReader(`{"classification":{"topics":["llm"],"form":"tool","use":"try"},"expected_revision":0,"operation_key":"confirm-7"}`))
	guarded.Header.Set("Content-Type", "application/json")
	guardedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(guardedResponse, guarded)
	if guardedResponse.Code != http.StatusOK || backend.curation.ExpectedRevision == nil ||
		*backend.curation.ExpectedRevision != 0 || backend.curation.OperationKey == nil ||
		*backend.curation.OperationKey != "confirm-7" {
		t.Fatalf("guarded confirmation was not forwarded: status=%d update=%+v", guardedResponse.Code, backend.curation)
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/bookmarks?view=summary&curation_status=kept&topic=llm&form=tool&use=try&source=x&uncertain=true&since=2026-09-01T00:00:00Z", nil)
	writer := httptest.NewRecorder()
	server.Handler().ServeHTTP(writer, request)
	if writer.Code != 200 || !backend.query.SummaryOnly || backend.query.CurationStatus != "kept" || backend.query.Topic != "llm" || backend.query.Form != "tool" || backend.query.Use != "try" || backend.query.Source != "x" || !backend.query.Uncertain || backend.query.Since != "2026-09-01T00:00:00Z" {
		t.Fatalf("facets were lost: %+v (HTTP %d)", backend.query, writer.Code)
	}
}

// The extensions endpoint must report every capability as off by default: a
// disabled extension is visible rather than a button that silently succeeds.
func TestExtensionsReportDisabledByDefault(t *testing.T) {
	fake := &fakeBackend{}
	server := New(context.Background(), health.NewTracker(), fake, nil, testLogger(), 1)
	defer server.Drain(time.Second)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/extensions", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"entities", "evidence", "rerank", "proposal"} {
		if payload[flag] != false {
			t.Errorf("%s = %v, want false", flag, payload[flag])
		}
	}
	if payload["quality_verified"] != false {
		t.Error("quality must not be claimed verified without gold")
	}
}
