package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

// No model boundary is involved: save ordinary links and real human actions,
// then query the actual Go dashboard over HTTP against the independent D1.
func TestLocalWorkerEffectiveClientFilters(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	ids := []int64{}
	for index, fields := range []map[string][]string{
		{"topics": {"llm", "eng", "eval", "design"}, "content_functions": {"method"}, "carriers": {"single"}, "affordances": {"practice"}},
		{"topics": {"eng"}, "content_functions": {"data"}, "carriers": {"external_article"}, "affordances": {"background"}},
	} {
		body, err := json.Marshal(map[string]string{"url": fmt.Sprintf("https://example.com/filter/%d", index)})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/links", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var created struct {
			ID int64 `json:"id"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&created)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusCreated || decodeErr != nil || created.ID == 0 {
			t.Fatalf("create %d: status=%d err=%v", index, response.StatusCode, decodeErr)
		}
		ids = append(ids, created.ID)
		for field, terms := range fields {
			for _, term := range terms {
				_, err := queue.ApplyV2Override(ctx, created.ID, cairn.V2Override{Field: field, Term: term, Action: "accept", OperationKey: fmt.Sprintf("filter-%d-%s-%s", index, field, term)})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	management := dashboard.New(ctx, health.NewTracker(), queue, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	defer management.Drain(time.Second)
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	get := func(path string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	for _, tc := range []struct {
		query  string
		ids    []int64
		total  int
		cursor *int64
	}{
		{"topics=design", ids[:1], 1, nil},
		{"content_functions=method,data&carriers=single&affordances=practice&entity_state=not_run", ids[:1], 1, nil},
		{"content_functions=method&carriers=external_article", []int64{}, 0, nil},
		{"entity_state=failed,stale", []int64{}, 0, nil},
		{"topic=design&topics=eng&limit=1", ids[1:], 2, &ids[1]},
		{fmt.Sprintf("topics=eng&limit=1&before_id=%d", ids[1]), ids[:1], 2, nil},
	} {
		status, body := get("/api/bookmarks?" + tc.query)
		var page cairn.BookmarkPage
		if err := json.Unmarshal(body, &page); err != nil || status != 200 {
			t.Fatalf("%s: status=%d body=%s err=%v", tc.query, status, body, err)
		}
		got := []int64{}
		for _, row := range page.Items {
			got = append(got, row.ID)
		}
		if !reflect.DeepEqual(got, tc.ids) || page.Counts.Total != tc.total || !reflect.DeepEqual(page.NextBeforeID, tc.cursor) || page.FilterContractVersion == nil || *page.FilterContractVersion != 1 {
			t.Fatalf("%s: ids=%v page=%+v", tc.query, got, page)
		}
	}
	for _, query := range []string{"topics=unknown", "topics=llm,,eng", "carriers=single&carriers=external_article", "entity_state=wrong", "filter_contract_version=2"} {
		for _, path := range []string{"/api/bookmarks?", "/api/export?"} {
			if status, body := get(path + query); status != 400 {
				t.Fatalf("%s%s: %d %s", path, query, status, body)
			}
		}
	}
	status, exported := get("/api/export?topics=design&content_functions=method&filter_contract_version=1")
	if status != 200 || !strings.Contains(string(exported), "/filter/0") || strings.Contains(string(exported), "/filter/1") {
		t.Fatalf("filtered export: %d %s", status, exported)
	}
	if _, err := queue.ApplyV2Override(ctx, ids[0], cairn.V2Override{Field: "topics", Term: "design", Action: "reject", OperationKey: "reject-filtered-topic"}); err != nil {
		t.Fatal(err)
	}
	status, body := get("/api/bookmarks?topics=design")
	var page cairn.BookmarkPage
	if err := json.Unmarshal(body, &page); err != nil || status != 200 || len(page.Items) != 0 || page.Counts.Total != 0 {
		t.Fatalf("confirmed human reject left stale membership: %d %s %v", status, body, err)
	}
	t.Log("real dashboard -> Go -> Worker/D1: fourth topic, OR/AND, entity states, cursors/counts, export, invalid queries and confirmed membership changes passed; zero model calls")
}
