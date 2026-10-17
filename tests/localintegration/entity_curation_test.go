package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

// Exercises the actual App-facing Go proxy and client against fresh Worker/D1.
// Entity state is a stored synthetic result; no classifier/provider is created.
func TestLocalWorkerEntityHumanCuration(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	id := createURLOnlyLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	client := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	app := dashboard.New(ctx, health.NewTracker(), client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	local := httptest.NewServer(app.Handler())
	defer local.Close()
	path := fmt.Sprintf("%s/api/bookmarks/%d", local.URL, id)
	type entityState struct {
		Status    string   `json:"status"`
		Automatic []string `json:"automatic"`
		Revision  int64    `json:"revision"`
		Values    []struct {
			Term      string `json:"term"`
			Origin    string `json:"origin"`
			Confirmed bool   `json:"confirmed"`
		} `json:"values"`
		Empty *struct {
			Origin    string `json:"origin"`
			Confirmed bool   `json:"confirmed"`
		} `json:"empty"`
	}
	type view struct {
		Revision int64 `json:"revision"`
		State    *struct {
			Entities *entityState `json:"entities"`
		} `json:"state"`
	}
	read := func() view {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, path+"/v2-selection", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var value view
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&value) != nil ||
			value.State == nil || value.State.Entities == nil {
			t.Fatalf("proxy omitted stored state: status=%d value=%+v", response.StatusCode, value)
		}
		return value
	}
	write := func(action, term, key string, expected int64, want int, replay bool) {
		t.Helper()
		body, err := json.Marshal(cairn.V2Override{Field: "entities", Action: action, Term: term,
			OperationKey: key, ExpectedRevision: &expected})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, path+"/v2-override", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var receipt map[string]any
		if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s returned %d: %v", action, response.StatusCode, receipt)
		}
		if want == http.StatusOK && (receipt["id"] != float64(id) || receipt["field"] != "entities" ||
			receipt["term"] != term || receipt["action"] != action || receipt["operation_key"] != key ||
			receipt["revision"] != float64(expected+1) || receipt["replayed"] != replay) {
			t.Fatalf("canonical receipt changed in transit: %v", receipt)
		}
	}
	initial := read()
	name := strings.Repeat("🙂", 60) // Existing Worker bound: 120 UTF-16 code units.
	write("accept", name, "long-name", initial.Revision, 200, false)
	write("accept", name, "long-name", initial.Revision, 200, true)
	current := read()
	if current.Revision != initial.Revision+1 || current.State.Entities.Status != "not_run" ||
		len(current.State.Entities.Automatic) != 0 || len(current.State.Entities.Values) != 1 ||
		current.State.Entities.Values[0].Term != name || current.State.Entities.Values[0].Origin != "human" ||
		!current.State.Entities.Values[0].Confirmed {
		t.Fatalf("manual name became an AI result or replay duplicated it: %+v", current.State.Entities)
	}
	write("accept", strings.Repeat("x", 121), "invalid-long", current.Revision, 400, false)
	write("reject", name, "stale-client", initial.Revision, 409, false)
	write("reject", name, "remove-long", current.Revision, 200, false)
	if len(read().State.Entities.Values) != 0 {
		t.Fatal("long entity name cannot be removed through the Go proxy")
	}
	snapshot := map[string]any{
		"blocks":     []map[string]any{{"id": "a", "role": "primary", "text": "Acme Tool"}},
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
	var saved struct {
		ID              int64  `json:"id"`
		ContentRevision int64  `json:"content_revision"`
		ContentHash     string `json:"content_hash"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if err := client.SubmitEntityState(ctx, id, map[string]any{
		"evidence_snapshot_id": saved.ID, "content_revision": saved.ContentRevision, "content_hash": saved.ContentHash,
		"operation_key": "synthetic-entities", "state": "completed_nonempty", "entities": []string{"Acme", "Tool"},
	}); err != nil {
		t.Fatal(err)
	}
	aiRevision := read().State.Entities.Revision
	for index, step := range []struct {
		action string
		term   string
		names  []string
	}{
		{"accept", "Manual", []string{"Acme", "Tool", "Manual"}},
		{"reject", "Acme", []string{"Tool", "Manual"}},
		{"set_empty", "", []string{}},
		{"reset", "Acme", []string{"Acme"}},
		{"reset", "", []string{"Acme", "Tool"}},
	} {
		current = read()
		write(step.action, step.term, fmt.Sprintf("human-%d", index), current.Revision, 200, false)
		entity := read().State.Entities
		names := make([]string, 0, len(entity.Values))
		for _, value := range entity.Values {
			names = append(names, value.Term)
		}
		if !slices.Equal(names, step.names) || entity.Status != "completed_nonempty" ||
			entity.Revision != aiRevision || !slices.Equal(entity.Automatic, []string{"Acme", "Tool"}) {
			t.Fatalf("%s changed independent AI baseline: %+v", step.action, entity)
		}
		if step.action == "set_empty" && (entity.Empty == nil || entity.Empty.Origin != "human" || !entity.Empty.Confirmed) {
			t.Fatal("explicit empty lost its human origin")
		}
	}
	snapshot["retrieval"] = "manual-newer"
	if err := client.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	current = read()
	write("accept", "Manual", "after-source-change", current.Revision, 200, false)
	entity := read().State.Entities
	if entity.Status != "stale" || len(entity.Automatic) != 0 || len(entity.Values) != 1 || entity.Values[0].Term != "Manual" {
		t.Fatalf("human curation upgraded stale AI output: %+v", entity)
	}
}
