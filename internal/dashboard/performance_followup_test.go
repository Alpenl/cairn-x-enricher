package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type followupBrowserBackend struct {
	modernCatalogFixture
	V2Backend
	secondAccount atomic.Bool
	legacy        atomic.Bool
}

func (b *followupBrowserBackend) GetV2Taxonomy(ctx context.Context) (cairn.V2Taxonomy, error) {
	return b.modernCatalogFixture.GetV2Taxonomy(ctx)
}

func (b *followupBrowserBackend) OfflineScope() string {
	if b.secondAccount.Load() {
		return strings.Repeat("c", 64)
	}
	return strings.Repeat("b", 64)
}

func (b *followupBrowserBackend) ListBookmarks(ctx context.Context, query cairn.BookmarkQuery) (cairn.BookmarkPage, error) {
	page, err := b.fakeBackend.ListBookmarks(ctx, query)
	page.Items = append([]cairn.Bookmark(nil), page.Items...)
	if !query.IncludeCacheIdentity || b.legacy.Load() {
		for i := range page.Items {
			page.Items[i].CacheIdentity = nil
		}
	}
	return page, err
}

func TestSummaryCacheIdentityIsExplicitAndForwarded(t *testing.T) {
	for _, option := range []string{"", "0", "1", "true", "01", "1&include_cache_identity=1"} {
		path := "/api/bookmarks?view=summary"
		if option != "" {
			path += "&include_cache_identity=" + option
		}
		query, err := bookmarkQuery(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		valid := option == "" || option == "0" || option == "1"
		if (err == nil) != valid || valid && query.IncludeCacheIdentity != (option == "1") {
			t.Fatalf("identity option %q: %+v %v", option, query, err)
		}
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("include_cache_identity") != "1" || r.URL.Query().Get("view") != "summary" {
			t.Error("identity or summary opt-in missing upstream")
		}
		_, _ = io.WriteString(w, `{"items":[],"next_before_id":null,"counts":{"total":0}}`)
	}))
	defer upstream.Close()
	server := New(t.Context(), startedTracker(), cairn.NewClient(upstream.URL, "fixture", upstream.Client()), &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/bookmarks?view=summary&include_cache_identity=1", nil))
	if response.Code != 200 || calls.Load() != 1 {
		t.Fatalf("list: status=%d upstream=%d", response.Code, calls.Load())
	}
}

func TestBrowserNoStoreDoesNotInvalidateVocabulary(t *testing.T) {
	var modernCalls atomic.Int32
	backend := &followupBrowserBackend{modernCatalogFixture: modernCatalogFixture{load: func(context.Context) (cairn.V2Taxonomy, error) {
		modernCalls.Add(1)
		return cairn.V2Taxonomy{Version: "fixture"}, nil
	}}}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for _, path := range []string{"/api/taxonomy", "/api/v2-taxonomy"} {
		for range 3 {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			r.Header.Set("Cache-Control", "no-cache")
			r.Header.Set("Pragma", "no-cache")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache response: %d %s", w.Code, w.Header())
			}
		}
	}
	if backend.taxonomyCalls != 1 || modernCalls.Load() != 1 {
		t.Fatal("normal browser reads bypassed application cache")
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2-taxonomy?refresh=1", nil))
	if w.Code != 200 || modernCalls.Load() != 2 {
		t.Fatal("explicit refresh did not invalidate")
	}
	server.catalog.now = func() time.Time { return time.Now().Add(taxonomyCacheTTL + time.Second) }
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2-taxonomy", nil))
	if w.Code != 200 || modernCalls.Load() != 3 {
		t.Fatal("expired vocabulary was reused")
	}
}

// Real Chrome headers, real embedded api/store and real Go handlers. Only the
// upstream data and the control endpoint are synthetic; no model/source calls.
func TestBrowserPerformanceFollowup(t *testing.T) {
	if os.Getenv("CAIRN_IMAGE_BROWSER") != "1" {
		t.Skip("set CAIRN_IMAGE_BROWSER=1 with Playwright and Chrome installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var modern, normalHeaders atomic.Int32
	backend := &followupBrowserBackend{modernCatalogFixture: modernCatalogFixture{load: func(context.Context) (cairn.V2Taxonomy, error) {
		modern.Add(1)
		return cairn.V2Taxonomy{Version: "fixture"}, nil
	}}}
	loaded := false
	identity := &cairn.BookmarkCacheIdentity{SchemaVersion: 1, ContentRevision: 1, BodyRevision: 1}
	backend.page = cairn.BookmarkPage{Items: []cairn.Bookmark{{ID: 1, URL: "https://example.com/1", Status: "completed", EnrichedAt: "2026-10-03", ContentLoaded: &loaded, CacheIdentity: identity}}}
	backend.detail = cairn.BookmarkDetail{Bookmark: backend.page.Items[0]}
	backend.detail.ContentLoaded = nil
	backend.detail.OriginalText = "original private body"
	backend.detail.TranslatedText = "translated private body"
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/followup-control":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			backend.secondAccount.Store(r.URL.Query().Get("account") == "2")
			backend.legacy.Store(r.URL.Query().Get("legacy") == "1")
			w.WriteHeader(http.StatusNoContent)
		case "/followup-probe":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<!doctype html><title>Local cache contract</title>")
		case "/followup-stats":
			backend.mu.Lock()
			legacy := backend.taxonomyCalls
			query := backend.query
			backend.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"legacy": legacy, "modern": modern.Load(), "no_cache_requests": normalHeaders.Load(), "identity_requested": query.IncludeCacheIdentity})
		default:
			if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Cache-Control") == "no-cache" {
				normalHeaders.Add(1)
			}
			handler.ServeHTTP(w, r)
		}
	}))
	defer probe.Close()
	command := exec.CommandContext(ctx, "node", "../../tests/browser/performance-followup.mjs")
	command.Env = append(os.Environ(), "CAIRN_FOLLOWUP_BASE="+probe.URL)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("browser: %v", err)
	}
}
