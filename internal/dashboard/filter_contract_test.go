package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

func TestBookmarkQueryRejectsMalformedV2Filters(t *testing.T) {
	for _, query := range []string{
		"content_functions=method,,data",
		"content_functions=method&content_functions=data",
		"topics=llm,",
		"carriers=",
		"affordances=practice,%27OR%201=1",
		"entity_state=failed,unknown",
	} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?"+query, nil)
			if _, err := bookmarkQuery(request); err == nil {
				t.Fatal("malformed v2 filter was silently ignored; query must be rejected")
			}
		})
	}
}

func TestOldWorkerCannotSilentlyIgnoreDashboardFilters(t *testing.T) {
	oldWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[],"next_before_id":null,"counts":{}}`)
	}))
	defer oldWorker.Close()
	ctx := context.Background()
	backend := cairn.NewClient(oldWorker.URL, "fixture", oldWorker.Client())
	server := New(ctx, health.NewTracker(), backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	defer server.Drain(time.Second)
	for _, path := range []string{"/api/bookmarks", "/api/export"} {
		for _, query := range []string{"topics=llm", "content_functions=method", "entity_state=failed", "form=method&filter_contract_version=1"} {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, path+"?"+query, nil))
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "unsupported_filter_contract") {
				t.Fatalf("%s?%s = %d %s", path, query, response.Code, response.Body)
			}
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("ordinary old backend browsing %s = %d %s", path, response.Code, response.Body)
		}
	}
}

func TestBookmarkQueryPreservesAllEffectiveDimensions(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?topics=llm,design,llm&content_functions=method,data&carriers=single,external_article&affordances=practice&entity_state=failed,stale&filter_contract_version=1", nil)
	query, err := bookmarkQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ got, want []string }{
		{query.Topics, []string{"llm", "design"}}, {query.ContentFunctions, []string{"method", "data"}},
		{query.Carriers, []string{"single", "external_article"}}, {query.Affordances, []string{"practice"}},
		{query.EntityStates, []string{"failed", "stale"}},
	} {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("got %v want %v", check.got, check.want)
		}
	}
	if !query.NeedsFilterContract() || !query.RequireEffectiveFilters {
		t.Fatal("missing explicit contract")
	}
}
