package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// countingBackend answers list calls by curation view so the overview can be
// checked against the exact query each count was read with.
type countingBackend struct {
	fakeBackend
	mu      sync.Mutex
	queries []cairn.BookmarkQuery
	totals  map[string]int
	fail    string
}

func (b *countingBackend) ListBookmarks(_ context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, query)
	view := query.CurationStatus
	if query.Uncertain {
		view = "uncertain"
	}
	if view == "" {
		view = "all"
	}
	if view == b.fail {
		return cairn.BookmarkPage{}, &cairn.APIError{StatusCode: http.StatusBadGateway, Code: "upstream"}
	}
	counts := cairn.BookmarkCounts{Total: b.totals[view]}
	if view == "all" {
		counts.Pending, counts.Processing, counts.Failed, counts.Exhausted = 2, 1, 3, 4
	}
	return cairn.BookmarkPage{Counts: counts}, nil
}

func (b *countingBackend) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queries)
}

func getOverview(t *testing.T, handler http.Handler) (int, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/overview", nil))
	var body map[string]any
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode overview: %v", err)
		}
	}
	return response.Code, body
}

func TestOverviewCountsEveryViewWithItsOwnFilter(t *testing.T) {
	backend := &countingBackend{totals: map[string]int{"all": 96, "inbox": 46, "kept": 28, "compiled": 9, "drop": 13, "uncertain": 48}}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)

	code, body := getOverview(t, server.Handler())
	if code != http.StatusOK {
		t.Fatalf("GET /api/overview = %d", code)
	}
	views, _ := body["views"].(map[string]any)
	for view, want := range backend.totals {
		if got, _ := views[view].(float64); int(got) != want {
			t.Errorf("views[%s] = %v, want %d", view, views[view], want)
		}
	}
	if body["attention"] != float64(7) || body["queued"] != float64(3) {
		t.Fatalf("attention/queued = %v/%v", body["attention"], body["queued"])
	}
	for _, query := range backend.queries {
		// Counts only need one summary row; a full page per view would be waste.
		if query.Limit != 1 || !query.SummaryOnly || query.Search != "" || query.NeedsFilterContract() {
			t.Fatalf("count query = %+v", query)
		}
	}
	if backend.calls() != len(overviewViews) {
		t.Fatalf("calls = %d, want %d", backend.calls(), len(overviewViews))
	}
}

func TestOverviewIsCachedAndInvalidatedByCuration(t *testing.T) {
	backend := &countingBackend{totals: map[string]int{"all": 3, "inbox": 3}}
	backend.detail = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{ID: 7}}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()

	getOverview(t, handler)
	getOverview(t, handler)
	if backend.calls() != len(overviewViews) {
		t.Fatalf("a poll within the TTL reached upstream: %d calls", backend.calls())
	}
	patch := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/api/bookmarks/7/curation", strings.NewReader(`{"curation_status":"kept"}`))
	patch.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, patch)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH curation = %d %s", response.Code, response.Body)
	}
	getOverview(t, handler)
	if backend.calls() != 2*len(overviewViews) {
		t.Fatalf("a curation change did not refresh the counts: %d calls", backend.calls())
	}
}

func TestOverviewReportsUpstreamFailure(t *testing.T) {
	backend := &countingBackend{totals: map[string]int{"all": 3}, fail: "kept"}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	code, _ := getOverview(t, server.Handler())
	if code != http.StatusBadGateway {
		t.Fatalf("GET /api/overview with a failing view = %d, want 502", code)
	}
}

func TestOverviewMarksLastGoodCountsStaleDuringUpstreamFailure(t *testing.T) {
	backend := &countingBackend{totals: map[string]int{"all": 3, "inbox": 3}}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()
	if code, _ := getOverview(t, handler); code != http.StatusOK {
		t.Fatalf("initial overview = %d", code)
	}
	backend.fail = "kept"
	server.overview.mu.Lock()
	server.overview.cachedAt = time.Now().Add(-overviewTTL - time.Second)
	server.overview.mu.Unlock()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/overview", nil))
	if response.Code != http.StatusOK || response.Header().Get("Warning") == "" {
		t.Fatalf("stale overview = %d, Warning %q", response.Code, response.Header().Get("Warning"))
	}
	var body overviewSummary
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Stale || body.Views["inbox"] != 3 {
		t.Fatalf("stale overview = %+v", body)
	}
}
