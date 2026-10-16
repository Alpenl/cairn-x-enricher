package localintegration

import (
	"context"
	"encoding/json"
	"errors"
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

// Actual Worker/D1 plus the Go client and App-facing management route. This
// source-only scenario has no classifier, provider fixture or external fetch.
func TestLocalWorkerEntitySourceRead(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	token := envOr("CAIRN_APP_TOKEN", "app")
	id := createURLOnlyLink(t, base, token)
	client := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	snapshot := map[string]any{
		"blocks":     []map[string]any{{"id": "a", "role": "quoted", "text": "前🙂Acme 后𠮷"}},
		"fetched_at": "2026-10-17T00:00:00Z", "retrieval": "manual",
		"truncation": map[string]any{"truncated": false},
	}
	if err := client.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := client.GetEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	var ref cairn.EntityEvidenceReference
	if json.Unmarshal(stored["id"], &ref.SnapshotID) != nil || json.Unmarshal(stored["content_revision"], &ref.ContentRevision) != nil {
		t.Fatal("stored immutable snapshot identity is missing")
	}
	ref.BlockID, ref.Start, ref.End = "a", 2, 6
	app := dashboard.New(ctx, health.NewTracker(), client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	local := httptest.NewServer(app.Handler())
	defer local.Close()
	read := func(want int) map[string]any {
		t.Helper()
		path := fmt.Sprintf("/api/bookmarks/%d/entity-evidence?snapshot_id=%d&content_revision=%d&block_id=a&start=2&end=6",
			id, ref.SnapshotID, ref.ContentRevision)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, local.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want || response.Header.Get("X-Cairn-Entity-Evidence") != "1" ||
			!strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Fatalf("source route lost status/capability/privacy: %d %v", response.StatusCode, response.Header)
		}
		var value map[string]any
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	value := read(200)
	if value["quote"] != "Acme" || value["before"] != "前🙂" || value["after"] != " 后𠮷" || value["current"] != true {
		t.Fatalf("Unicode source changed in transit: %v", value)
	}
	bad := ref
	bad.ContentRevision++
	if _, err := client.GetEntityEvidence(ctx, id, bad); !errors.Is(err, cairn.ErrEntityEvidenceChanged) {
		t.Fatalf("revision mismatch: %v", err)
	}
	if _, err := client.GetEntityEvidence(ctx, id+1, ref); !errors.Is(err, cairn.ErrEntityEvidenceUnavailable) {
		t.Fatalf("foreign snapshot became readable: %v", err)
	}
	snapshot["blocks"] = []map[string]any{{"id": "a", "role": "primary", "text": "NEW source must not substitute the stored quote"}}
	if err := client.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	old := read(200)
	if old["quote"] != "Acme" || old["current"] != false {
		t.Fatal("old immutable source was replaced or upgraded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/links/%d", base, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete: %d", response.StatusCode)
	}
	read(404)
}
