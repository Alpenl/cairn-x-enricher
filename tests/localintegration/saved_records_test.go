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

func TestLocalWorkerSavedRecords(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	token := envOr("CAIRN_APP_TOKEN", "app")
	id := createURLOnlyLink(t, base, token)
	payload := `{"blocks":[{"id":"original","text":"` + strings.Repeat("x", 17000) + `"}],"unknown":null,"complete":false}`
	// Isolated synthetic history, not production material or quality labels.
	localFixtureSQL(ctx, t, fmt.Sprintf(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<55)
INSERT INTO evidence_snapshots(link_id,content_revision,content_hash,payload,completeness,created_at)
SELECT %[1]d,x,'saved-hash','%[2]s','partial','2026-10-17T00:00:00Z' FROM n;
WITH RECURSIVE n(x) AS (SELECT 0 UNION ALL SELECT x+1 FROM n WHERE x<54)
INSERT INTO entity_operations(operation_key,link_id,request_hash,evidence_snapshot_id,content_revision,content_hash,payload,outcome,applied,created_at)
SELECT printf('op-%%03d',x),%[1]d,'saved-request',(SELECT id FROM evidence_snapshots WHERE link_id=%[1]d AND content_revision=55),55,
'saved-hash','{"state":"failed","entities":[],"unknown":null}','ignored_stale',1,'2026-10-17T00:00:00Z' FROM n;
INSERT INTO curation_overrides(link_id,field,term,action,source,confirmed,revision,operation_key,created_at)
VALUES (%[1]d,'entities','旧名','reject','legacy_unknown',0,1,'saved-override','2026-10-17T00:00:00Z');
INSERT INTO curation_events(link_id,kind,payload,revision,operation_key,created_at)
VALUES (%[1]d,'why','{"why":"原备注","unknown":null}',1,'saved-event','2026-10-17T00:00:00Z');`, id, payload))
	client := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	app := dashboard.New(ctx, health.NewTracker(), client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	read := func(origin, suffix string, status int) map[string]json.RawMessage {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/bookmarks/%d/saved-records/%s", origin, id, suffix), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Cairn-App-Saved-Records", "1")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != status || response.Header.Get("X-Cairn-App-Saved-Records") != "1" || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Fatalf("saved history contract: %d %v", response.StatusCode, response.Header)
		}
		var value map[string]json.RawMessage
		if json.NewDecoder(response.Body).Decode(&value) != nil {
			t.Fatal("invalid saved response")
		}
		return value
	}
	for _, origin := range []string{base, server.URL} {
		for _, kind := range []string{"sources", "entities?source_revision=55"} {
			page := read(origin, kind, 200)
			var rows []map[string]json.RawMessage
			if json.Unmarshal(page["records"], &rows) != nil || len(rows) != 50 {
				t.Fatal("unbounded or missing page")
			}
			var next string
			if json.Unmarshal(page["next_after"], &next) != nil {
				t.Fatal("missing cursor")
			}
			separator := "?"
			if strings.Contains(kind, "?") {
				separator = "&"
			}
			older := read(origin, kind+separator+"after_key="+next, 200)
			if json.Unmarshal(older["records"], &rows) != nil || len(rows) != 5 || string(older["next_after"]) != "null" {
				t.Fatal("keyset page failed")
			}
		}
		source := read(origin, "sources?record_key=55", 200)
		var record map[string]json.RawMessage
		if json.Unmarshal(source["record"], &record) != nil || string(record["payload"]) != payload {
			t.Fatal("complete source changed")
		}
		entity := read(origin, "entities?source_revision=55&record_key=op-054", 200)
		if json.Unmarshal(entity["record"], &record) != nil || string(record["outcome"]) != `"ignored_stale"` || string(record["state"]) != `"failed"` {
			t.Fatal("ignored receipt became AI success")
		}
		manual := read(origin, "overrides?record_key=1", 200)
		if json.Unmarshal(manual["record"], &record) != nil || string(record["confirmed"]) != "0" || string(record["source"]) != `"legacy_unknown"` {
			t.Fatal("legacy row upgraded to human")
		}
		read(origin, "events?record_key=1", 200)
		read(origin, "entities?source_revision=54&record_key=op-054", 404)
	}
	r, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("%s/api/links/%d", base, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("delete failed")
	}
	for _, origin := range []string{base, server.URL} {
		read(origin, "sources?record_key=55", 404)
		read(origin, "entities?source_revision=55", 404)
	}
	t.Log("Actual App Worker and Go proxy preserve full original source and historical unknown/manual/ignored rows, page 50+5, reject wrong source and deleted links; no provider constructed")
}
