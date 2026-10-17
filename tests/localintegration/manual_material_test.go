package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerManualMaterialRecoveryAndClassification(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}},
		Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var calls atomic.Int64
	providerHTTP := provider.Client()
	transport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			State classify.Evidence `json:"state"`
		}
		if json.Unmarshal(body, &request) != nil || request.State.Primary != "Original objective source" || len(request.State.Context) != 1 {
			t.Error("bound material did not reach actual provider state")
		} else {
			block := request.State.Context[0]
			if block.Role != classify.RoleLegacyUnknown || block.Acquired != "manual" || block.Text != "Human transcribed image 🙂" ||
				!strings.Contains(block.Relation, `"partial":true`) || !strings.Contains(block.Relation, `"why_needed":"image_text"`) {
				t.Error("manual provenance, uncertainty or exact fragment was lost")
			}
		}
		if strings.Contains(string(body), "private human explanation") {
			t.Error("personal state leaked into objective input")
		}
		return transport.RoundTrip(r)
	})
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", providerHTTP, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	id := createURLOnlyLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	image := fmt.Sprintf("enrichment/%d/%s.png", id, strings.Repeat("a", 64))
	localFixtureSQL(ctx, t, fmt.Sprintf(`UPDATE links SET original_text='Original objective source',enrichment_status='completed',
ai_title='Keep title',summary='Keep summary',translated_text='Keep translation',images='["%s"]',why='private human explanation',personal_revision=1 WHERE id=%d;
INSERT INTO curation_overrides(link_id,field,term,action,source,confirmed,revision,operation_key,created_at)
VALUES (%d,'use','contra','accept','human',1,1,'manual-material-human','2026-10-17');`, image, id, id))
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(enrich.Source{OriginalText: "Original objective source", Model: "manual"}, time.Now())); err != nil {
		t.Fatal(err)
	}
	localFixtureSQL(ctx, t, fmt.Sprintf("UPDATE classification_jobs SET status='completed',target_generation=1 WHERE link_id=%d", id))
	var posts atomic.Int64
	faultHTTP := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/manual-material") && posts.Add(1) == 1 {
			_ = response.Body.Close()
			return nil, errors.New("synthetic response lost after actual Worker commit")
		}
		return response, err
	})}
	backend := cairn.NewClient(base, token, faultHTTP)
	app := dashboard.New(ctx, nil, backend, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	send := func(method string, intent *cairn.ManualMaterialIntent) (int, json.RawMessage) {
		t.Helper()
		var data []byte
		if intent != nil {
			data, _ = json.Marshal(intent)
		}
		r, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("%s/api/bookmarks/%d/manual-material", server.URL, id), bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Cairn-App-Manual-Material", "1")
		response, err := http.DefaultClient.Do(r)
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
	status, raw := send("GET", nil)
	var options struct {
		CanSubmit bool `json:"can_submit"`
		Source    struct {
			Revision   int64  `json:"content_revision"`
			SnapshotID int64  `json:"evidence_snapshot_id"`
			Hash       string `json:"source_hash"`
			Generation int64  `json:"target_generation"`
		} `json:"source"`
	}
	if status != 200 || json.Unmarshal(raw, &options) != nil || !options.CanSubmit || calls.Load() != 0 {
		t.Fatal("ordinary read processed material")
	}
	intent := cairn.ManualMaterialIntent{Version: 1, OperationKey: "manual-material-recovery", ExpectedContentRevision: options.Source.Revision,
		EvidenceSnapshotID: options.Source.SnapshotID, SourceHash: options.Source.Hash, TargetGeneration: options.Source.Generation, Kind: "image_text", SourceRef: image, Text: "Human transcribed image 🙂\n "}
	if status, _ := send("POST", &intent); status < 500 || calls.Load() != 0 {
		t.Fatal("lost result was reported as success or called provider")
	}
	status, raw = send("POST", &intent)
	var receipt struct {
		Replayed, Changed bool
		Queued            bool  `json:"classification_queued"`
		Revision          int64 `json:"content_revision"`
	}
	if status != 200 || json.Unmarshal(raw, &receipt) != nil || !receipt.Replayed || !receipt.Changed || !receipt.Queued || receipt.Revision != options.Source.Revision+1 || posts.Load() != 2 || calls.Load() != 0 {
		t.Fatalf("same intent failed to confirm exact receipt: %d %s", status, raw)
	}
	archived, err := queue.GetEvidence(ctx, id)
	var archive struct {
		Snapshot struct {
			Blocks []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"snapshot"`
	}
	if err != nil || json.Unmarshal(archived, &archive) != nil || len(archive.Snapshot.Blocks) != 2 || archive.Snapshot.Blocks[1].Text != intent.Text {
		t.Fatal("archive changed original human fragment bytes")
	}
	// Existing provider preparation trims surrounding whitespace. It must keep
	// the source archive exact and retain manual/partial provenance in wire state.
	p := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	done, failed, err := p.RunClassifications(ctx, 1)
	if err != nil || done != 1 || failed != 0 || calls.Load() != 1 {
		t.Fatalf("manual input not processed: %d/%d/%d %v", done, failed, calls.Load(), err)
	}
	if done, _, err := p.RunClassifications(ctx, 1); err != nil || done != 0 || calls.Load() != 1 {
		t.Fatal("replayed material caused another paid call")
	}
	readable, err := queue.GetBookmark(ctx, id)
	if err != nil || readable.AITitle != "Keep title" || readable.Summary != "Keep summary" || readable.TranslatedText != "Keep translation" {
		t.Fatalf("reading aids changed: %v", err)
	}
	effective, err := queue.GetV2Effective(ctx, id)
	if err != nil || !strings.Contains(string(effective), `"use":"contra"`) {
		t.Fatalf("human override changed: %s %v", effective, err)
	}
	status, raw = send("POST", &intent)
	if status != 200 || !bytes.Contains(raw, []byte(`"replayed":true`)) || calls.Load() != 1 {
		t.Fatal("late retry did not return original receipt")
	}
	t.Log("Actual Worker/D1 + Go gateway: post-commit response loss confirms same receipt; one claimable new snapshot -> one contract-model call with manual/partial provenance; original reading and human use retained")
}
