package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

func TestLocalWorkerAppRunHistory(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	token := envOr("CAIRN_APP_TOKEN", "app")
	id := createURLOnlyLink(t, base, token)
	// Only the isolated test D1 is seeded. The records are explicitly synthetic;
	// no classifier, model fixture, budget reservation or provider is created.
	localFixtureSQL(ctx, t, fmt.Sprintf(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<55)
 INSERT INTO classification_runs(link_id,content_revision,spec_id,spec_hash,target_generation,requested_model,
 resolved_model,policy_version,answers,usage,raw_judgments,operation_key,status,coverage,evidence_coverage,created_at)
 SELECT %d,1,'saved-spec','saved-hash',0,'saved-request','','saved-policy','{"topic":false}',
 '{"input_tokens":17}','{"source":{"partial":true}}','history-'||x,'partial','partial','partial','2026-10-17T00:00:00Z' FROM n`, id))
	client := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	app := dashboard.New(ctx, health.NewTracker(), client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	local := httptest.NewServer(app.Handler())
	defer local.Close()
	read := func(origin, suffix, credential string, want int) map[string]json.RawMessage {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/bookmarks/%d/run-history%s", origin, id, suffix), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Cairn-App-Run-History", "1")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want || response.Header.Get("X-Cairn-App-Run-History") != "1" || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Fatalf("history contract changed: %d %v", response.StatusCode, response.Header)
		}
		var value map[string]json.RawMessage
		if json.NewDecoder(response.Body).Decode(&value) != nil {
			t.Fatal("invalid history response")
		}
		return value
	}
	for _, origin := range []string{base, local.URL} {
		page := read(origin, "", token, 200)
		var rows []map[string]json.RawMessage
		if json.Unmarshal(page["runs"], &rows) != nil || len(rows) != 50 || string(page["next_after_id"]) != "6" {
			t.Fatal("history was not bounded/newest first")
		}
		for _, row := range rows {
			if _, exists := row["raw_judgments"]; exists {
				t.Fatal("summary loaded raw judgments")
			}
		}
		older := read(origin, "?after_id=6", token, 200)
		if json.Unmarshal(older["runs"], &rows) != nil || len(rows) != 5 || string(older["next_after_id"]) != "null" {
			t.Fatal("keyset page did not terminate")
		}
		detail := read(origin, "/55", token, 200)
		var record map[string]json.RawMessage
		if json.Unmarshal(detail["run"], &record) != nil || string(record["resolved_model"]) != `""` ||
			string(record["answers"]) != `{"topic":false}` || string(record["usage"]) != `{"input_tokens":17}` ||
			string(record["raw_judgments"]) != `{"source":{"partial":true}}` || string(record["status"]) != `"partial"` {
			t.Fatal("saved unknown model, partial answers, usage or source changed")
		}
		read(origin, "/999", token, 404)
	}
	deletion, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/links/%d", base, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	deletion.Header.Set("Authorization", "Bearer "+token)
	deleted, err := http.DefaultClient.Do(deletion)
	if err != nil {
		t.Fatal(err)
	}
	_ = deleted.Body.Close()
	if deleted.StatusCode != 204 {
		t.Fatalf("delete: %d", deleted.StatusCode)
	}
	for _, origin := range []string{base, local.URL} {
		read(origin, "", token, 404)
		read(origin, "/55", token, 404)
	}
	t.Log("Actual authenticated App Worker and Go proxy read bounded metadata and one stored record; unknown model stays unknown; deletion is not replaced with another run; no model provider was constructed")
}
