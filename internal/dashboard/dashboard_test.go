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
)

type fakeBackend struct {
	mu        sync.Mutex
	query     cairn.BookmarkQuery
	page      cairn.BookmarkPage
	detail    cairn.BookmarkDetail
	jobs      map[int64]*cairn.Job
	claimErrs map[int64]error
}

func (b *fakeBackend) ListBookmarks(_ context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.query = query
	return b.page, nil
}

func (b *fakeBackend) GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error) {
	return b.detail, nil
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
}

func (p *fakeProcessor) Process(_ context.Context, job *cairn.Job) error {
	p.processed <- job.ID
	return nil
}

func TestHandlerServesChineseDashboardAndBookmarkData(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{
		page: cairn.BookmarkPage{
			Items: []cairn.Bookmark{{
				ID: 7, URL: "https://x.com/example/status/7", Note: "测试收藏",
				CreatedAt: "2026-09-03T00:00:00Z", Status: "completed", Summary: "测试总结",
				RelatedURLs: []string{},
			}},
			Counts: cairn.BookmarkCounts{Total: 1, Completed: 1},
		},
		detail: cairn.BookmarkDetail{
			Bookmark: cairn.Bookmark{
				ID: 7, URL: "https://x.com/example/status/7", Status: "completed", RelatedURLs: []string{},
			},
			OriginalText: "完整原文",
		},
		jobs:      map[int64]*cairn.Job{},
		claimErrs: map[int64]error{},
	}
	server := New(ctx, health.NewTracker(), backend, &fakeProcessor{processed: make(chan int64, 1)}, testLogger(), 1)

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "Cairn X 收藏处理台") {
		t.Fatalf("GET / = %d %q", root.Code, root.Body.String())
	}
	if root.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("GET / is missing Content-Security-Policy")
	}

	list := httptest.NewRecorder()
	server.Handler().ServeHTTP(list, httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"/api/bookmarks?limit=10&status=completed&q=%E6%B5%8B%E8%AF%95",
		nil,
	))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "测试总结") {
		t.Fatalf("GET /api/bookmarks = %d %q", list.Code, list.Body.String())
	}
	if backend.query.Limit != 10 || backend.query.Status != "completed" || backend.query.Search != "测试" {
		t.Fatalf("bookmark query = %+v", backend.query)
	}

	detail := httptest.NewRecorder()
	server.Handler().ServeHTTP(detail, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/bookmarks/7", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "完整原文") {
		t.Fatalf("GET /api/bookmarks/7 = %d %q", detail.Code, detail.Body.String())
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
	server := New(ctx, tracker, backend, &fakeProcessor{processed: processed}, testLogger(), 1)

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

func TestHandlerRejectsInvalidManagementRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, health.NewTracker(), backend, &fakeProcessor{processed: make(chan int64, 1)}, testLogger(), 1)

	for _, test := range []struct {
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{http.MethodGet, "/api/bookmarks?limit=99", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/bookmarks/0", "", "", http.StatusBadRequest},
		{http.MethodPost, "/api/bookmarks/process", `{"ids":[]}`, "application/json", http.StatusBadRequest},
		{http.MethodPost, "/api/bookmarks/process", `{"ids":[1]}`, "text/plain", http.StatusBadRequest},
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
