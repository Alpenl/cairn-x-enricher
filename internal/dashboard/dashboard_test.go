package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type fakeBackend struct {
	mu        sync.Mutex
	query     cairn.BookmarkQuery
	page      cairn.BookmarkPage
	pages     map[string]cairn.BookmarkPage
	detail    cairn.BookmarkDetail
	jobs      map[int64]*cairn.Job
	claimErrs map[int64]error
	imageBody string
}

func (b *fakeBackend) ListBookmarks(_ context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

func (b *fakeBackend) GetImage(context.Context, string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  []string{"image/jpeg"},
			"Cache-Control": []string{"private, max-age=86400"},
			"ETag":          []string{`"test-image"`},
		},
		Body: io.NopCloser(strings.NewReader(b.imageBody)),
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
	server := New(ctx, health.NewTracker(), backend, &fakeProcessor{processed: make(chan int64, 1), sources: make(chan sourceProcess, 1)}, testLogger(), 1)

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "Cairn 收藏") {
		t.Fatalf("GET / = %d %q", root.Code, root.Body.String())
	}
	for _, label := range []string{"搜索标题、备注、摘要或译文", "/assets/home.js", "/backstage"} {
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

	reader := httptest.NewRecorder()
	server.Handler().ServeHTTP(reader, httptest.NewRequestWithContext(ctx, http.MethodGet, "/bookmarks/7", nil))
	if reader.Code != http.StatusOK {
		t.Fatalf("GET /bookmarks/7 = %d", reader.Code)
	}
	for _, label := range []string{"展开原文", "下一条", "/assets/reader.js"} {
		if !strings.Contains(reader.Body.String(), label) {
			t.Errorf("GET /bookmarks/7 does not contain %q", label)
		}
	}

	backstage := httptest.NewRecorder()
	server.Handler().ServeHTTP(backstage, httptest.NewRequestWithContext(ctx, http.MethodGet, "/backstage", nil))
	if backstage.Code != http.StatusOK || !strings.Contains(backstage.Body.String(), "/assets/backstage.js") {
		t.Fatalf("GET /backstage = %d %q", backstage.Code, backstage.Body.String())
	}

	script := httptest.NewRecorder()
	server.Handler().ServeHTTP(script, httptest.NewRequestWithContext(ctx, http.MethodGet, "/assets/home.js", nil))
	if script.Code != http.StatusOK || !strings.Contains(script.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("GET /assets/home.js = %d %q", script.Code, script.Header().Get("Content-Type"))
	}

	stylesheet := httptest.NewRecorder()
	server.Handler().ServeHTTP(stylesheet, httptest.NewRequestWithContext(ctx, http.MethodGet, "/assets/dashboard.css", nil))
	if stylesheet.Code != http.StatusOK || !strings.Contains(stylesheet.Header().Get("Content-Type"), "text/css") || !strings.Contains(stylesheet.Body.String(), ".feature") {
		t.Fatalf("GET /assets/dashboard.css = %d %q", stylesheet.Code, stylesheet.Header().Get("Content-Type"))
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
	backend := &fakeBackend{
		jobs: map[int64]*cairn.Job{
			1: {ID: 1, URL: "https://x.com/example/status/1", Attempt: 1, LeaseToken: "lease-1"},
		},
		claimErrs: map[int64]error{
			2: &cairn.APIError{StatusCode: http.StatusConflict, Code: "job_busy"},
		},
	}
	processed := make(chan int64, 1)
	tracker := health.NewTracker()
	server := New(ctx, tracker, backend, &fakeProcessor{processed: processed, sources: make(chan sourceProcess, 1)}, testLogger(), 1)

	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/api/bookmarks/process",
		strings.NewReader(`{"ids":[1,2,1]}`),
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
	if len(body.Rejected) != 1 || body.Rejected[0].ID != 2 || body.Rejected[0].Error != "job_busy" {
		t.Fatalf("rejected = %+v", body.Rejected)
	}

	select {
	case id := <-processed:
		if id != 1 {
			t.Fatalf("processed ID = %d", id)
		}
	case <-time.After(time.Second):
		t.Fatal("manual job was not processed")
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
	tracker := health.NewTracker()
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

func TestHandlerQueuesManualSourceText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		jobs: map[int64]*cairn.Job{
			20: {ID: 20, URL: "https://x.com/example/status/20", Attempt: 6, LeaseToken: "lease-20"},
		},
		claimErrs: map[int64]error{},
	}
	sources := make(chan sourceProcess, 1)
	server := New(ctx, health.NewTracker(), backend, &fakeProcessor{
		processed: make(chan int64, 1),
		sources:   sources,
	}, testLogger(), 1)

	request := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/api/bookmarks/20/source",
		strings.NewReader(`{"original_text":" 人工粘贴原文 "}`),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("POST /api/bookmarks/20/source status = %d, body = %s", response.Code, response.Body.String())
	}

	select {
	case got := <-sources:
		if got.ID != 20 || got.SourceText != "人工粘贴原文" {
			t.Fatalf("source process = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("manual source job was not processed")
	}
}

func TestHandlerRejectsInvalidManagementRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, health.NewTracker(), backend, &fakeProcessor{processed: make(chan int64, 1), sources: make(chan sourceProcess, 1)}, testLogger(), 1)

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

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
