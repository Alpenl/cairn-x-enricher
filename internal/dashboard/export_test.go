package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// pagingBackend serves a descending run of bookmark IDs through cursor pages,
// like the Worker, and records every list query it receives.
type pagingBackend struct {
	fakeBackend
	mu      sync.Mutex
	total   int
	queries []cairn.BookmarkQuery
}

func (b *pagingBackend) ListBookmarks(_ context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, query)
	start := int64(b.total)
	if query.BeforeID > 0 {
		start = query.BeforeID - 1
	}
	page := cairn.BookmarkPage{Counts: cairn.BookmarkCounts{Total: b.total}}
	for id := start; id >= 1 && len(page.Items) < query.Limit; id-- {
		page.Items = append(page.Items, cairn.Bookmark{ID: id, URL: fmt.Sprintf("https://x.com/a/status/%d", id), CurationStatus: "kept", AITitle: fmt.Sprintf("标题 %d", id)})
	}
	if last := len(page.Items); last > 0 && page.Items[last-1].ID > 1 {
		next := page.Items[last-1].ID
		page.NextBeforeID = &next
	}
	return page, nil
}

func exportBody(t *testing.T, handler http.Handler, query string) (int, string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/export?"+query, nil))
	return response.Code, response.Body.String()
}

func TestExportPagesBeyondOneListPage(t *testing.T) {
	backend := &pagingBackend{total: 260}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)

	code, body := exportBody(t, server.Handler(), "limit=150&curation_status=kept")
	if code != http.StatusOK {
		t.Fatalf("export = %d %s", code, body)
	}
	if got := strings.Count(body, "- 收藏 ID："); got != 150 {
		t.Fatalf("exported %d bookmarks, want 150", got)
	}
	if !strings.Contains(body, "共 150 条") || !strings.Contains(body, "已达到单次导出上限 150 条") {
		t.Fatalf("export does not state its size and truncation:\n%s", body[:300])
	}
	if !strings.Contains(body, "收藏 ID：260\n") || !strings.Contains(body, "收藏 ID：111\n") || strings.Contains(body, "收藏 ID：110\n") {
		t.Fatal("export did not keep the newest-first cursor order")
	}
	for _, query := range backend.queries {
		if query.Limit > exportPageSize || !query.SummaryOnly || query.CurationStatus != "kept" {
			t.Fatalf("export list query = %+v", query)
		}
	}
	if len(backend.queries) != 2 {
		t.Fatalf("export used %d list calls, want 2", len(backend.queries))
	}
}

func TestExportDefaultsAndBounds(t *testing.T) {
	backend := &pagingBackend{total: 30}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)

	code, body := exportBody(t, server.Handler(), "")
	if code != http.StatusOK || strings.Count(body, "- 收藏 ID：") != 30 || strings.Contains(body, "上限") {
		t.Fatalf("unbounded small export = %d, %d items", code, strings.Count(body, "- 收藏 ID："))
	}
	for _, query := range []string{"limit=0", "limit=501", "limit=abc"} {
		if code, _ := exportBody(t, server.Handler(), query); code != http.StatusBadRequest {
			t.Fatalf("export?%s = %d, want 400", query, code)
		}
	}
}
