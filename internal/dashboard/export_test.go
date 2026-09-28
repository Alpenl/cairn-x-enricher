package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type batchExportBackend struct {
	*pagingBackend
	*cairn.Client
}

func (b *batchExportBackend) ListBookmarks(ctx context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	return b.pagingBackend.ListBookmarks(ctx, query)
}

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
	if !strings.Contains(body, "部分/过期结果：150 条") ||
		strings.Count(body, "有效结果：不可用") != 150 {
		t.Fatal("v1-only export hid unavailable effective views")
	}
	if !strings.Contains(body, "收藏 ID：260\n") || !strings.Contains(body, "收藏 ID：111\n") || strings.Contains(body, "收藏 ID：110\n") {
		t.Fatal("export did not keep the newest-first cursor order")
	}
	for _, query := range backend.queries {
		if query.Limit > exportPageSize || !query.SummaryOnly || !query.SkipCounts || query.CurationStatus != "kept" {
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

func TestExportUsesBatchesAndFallsBackForOldWorker(t *testing.T) {
	for _, oldWorker := range []bool{false, true} {
		t.Run(fmt.Sprintf("old=%t", oldWorker), func(t *testing.T) {
			const exportCount = 500
			var batchCalls, singleCalls atomic.Int64
			worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v2/links/effective-batch":
					batchCalls.Add(1)
					if oldWorker {
						http.NotFound(writer, request)
						return
					}
					var body struct {
						IDs []int64 `json:"ids"`
					}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Errorf("batch body: %v", err)
					}
					if len(body.IDs) > 50 {
						t.Errorf("batch too large: %d", len(body.IDs))
					}
					items := make([]map[string]any, 0, len(body.IDs))
					for _, id := range body.IDs {
						items = append(items, map[string]any{"id": id, "effective": map[string]any{
							"topics": []string{"llm"}, "reviewed": true,
						}, "projected": true, "stale": false})
					}
					_ = json.NewEncoder(writer).Encode(map[string]any{"version": 1, "items": items,
						"missing_ids": []int64{}, "d1": map[string]any{"scope": "effective_view_only", "sql_count": 1, "rows_read": len(items), "rows_written": 0}})
				default:
					var id int64
					if _, err := fmt.Sscanf(request.URL.Path, "/api/v2/links/%d/effective", &id); err != nil {
						http.NotFound(writer, request)
						return
					}
					singleCalls.Add(1)
					_ = json.NewEncoder(writer).Encode(map[string]any{"id": id, "effective": map[string]any{
						"topics": []string{"llm"}, "reviewed": true,
					}, "projected": true, "stale": false})
				}
			}))
			defer worker.Close()
			backend := &batchExportBackend{pagingBackend: &pagingBackend{total: exportCount},
				Client: cairn.NewClient(worker.URL, "internal", worker.Client())}
			server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
			defer server.Drain(time.Second)
			code, body := exportBody(t, server.Handler(), "limit=500")
			if code != http.StatusOK || strings.Count(body, "- 主题：llm") != exportCount {
				t.Fatalf("export = %d, %d effective topics", code, strings.Count(body, "- 主题：llm"))
			}
			if oldWorker {
				if batchCalls.Load() != 1 || singleCalls.Load() != exportCount {
					t.Fatalf("old Worker calls: batch %d, single %d", batchCalls.Load(), singleCalls.Load())
				}
			} else if batchCalls.Load() != 10 || singleCalls.Load() != 0 {
				t.Fatalf("new Worker calls: batch %d, single %d", batchCalls.Load(), singleCalls.Load())
			}
		})
	}
}

func TestExportDoesNotReturnPartialFileWhenBatchFails(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v2/links/effective-batch" {
			t.Errorf("unexpected fallback request: %s", request.URL.Path)
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":"backend_unavailable"}`))
	}))
	defer worker.Close()
	backend := &batchExportBackend{pagingBackend: &pagingBackend{total: 2},
		Client: cairn.NewClient(worker.URL, "internal", worker.Client())}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	code, body := exportBody(t, server.Handler(), "")
	if code == http.StatusOK || strings.Contains(body, "# Cairn 收藏导出") {
		t.Fatalf("failed batch produced a partial export: %d %s", code, body)
	}
}
