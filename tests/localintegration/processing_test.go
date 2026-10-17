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
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerProcessingSourceAndIndependentStages(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}},
		Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var modelCalls atomic.Int64
	providerHTTP := provider.Client()
	transport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		modelCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var payload struct {
			State classify.Evidence `json:"state"`
		}
		if json.Unmarshal(body, &payload) != nil || payload.State.Primary != "Manually supplied original 🙂" || len(payload.State.Context) != 1 ||
			payload.State.Context[0].Role != classify.RoleLegacyUnknown || payload.State.Context[0].Text != "Unknown image context" ||
			bytes.Contains(body, []byte("Private human explanation")) {
			t.Error("actual provider input lost original/unknown roles or leaked personal state")
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
	localFixtureSQL(ctx, t, fmt.Sprintf(`UPDATE links SET original_text=NULL,enrichment_status='completed',ai_title='Keep title',summary='Keep summary',
translated_text='Keep translation',images='[{"key":"%s","content_type":"image/png"}]',why='Private human explanation',personal_revision=1 WHERE id=%d;
INSERT INTO curation_overrides(link_id,field,term,action,source,confirmed,revision,operation_key,created_at)
VALUES (%d,'use','contra','accept','human',1,1,'processing-human','2026-10-17');`, image, id, id))
	if err := queue.SubmitEvidence(ctx, id, map[string]any{"blocks": []map[string]any{{"id": "unknown", "role": "legacy_unknown", "text": "Unknown image context", "acquired": "browser"}},
		"fetched_at": "2026-10-17", "retrieval": "browser", "truncation": map[string]any{"truncated": false}}); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int64
	faultHTTP := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/processing") && posts.Add(1) == 1 {
			_ = response.Body.Close()
			return nil, errors.New("synthetic response loss after actual processing commit")
		}
		return response, err
	})}
	gateway := dashboard.New(ctx, nil, cairn.NewClient(base, token, faultHTTP), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	send := func(intent *cairn.ProcessingIntent) (int, []byte) {
		t.Helper()
		method := "GET"
		var body []byte
		if intent != nil {
			method = "POST"
			body, _ = json.Marshal(intent)
		}
		r, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("%s/api/bookmarks/%d/processing", server.URL, id), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(cairn.ProcessingCapability, "1")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	type statusView struct {
		Fence      string `json:"fence"`
		Revision   int64  `json:"content_revision"`
		Generation int64  `json:"target_generation"`
	}
	status, data := send(nil)
	var view statusView
	if status != 200 || json.Unmarshal(data, &view) != nil || !bytes.Contains(data, []byte(`"state":"missing_primary"`)) || modelCalls.Load() != 0 {
		t.Fatalf("missing-primary capability was incorrect: %d %s", status, data)
	}
	text := " Manually supplied original 🙂\n "
	intent := cairn.ProcessingIntent{Version: 1, OperationKey: "source-recovery", Fence: view.Fence, Action: "save_primary",
		ExpectedContentRevision: view.Revision, TargetGeneration: view.Generation, Text: &text}
	if status, _ = send(&intent); status < 500 || posts.Load() != 1 || modelCalls.Load() != 0 {
		t.Fatal("unknown submission was retried or returned a false acknowledgement")
	}
	status, data = send(&intent)
	if status != 200 || !bytes.Contains(data, []byte(`"replayed":true`)) || posts.Load() != 2 || modelCalls.Load() != 0 {
		t.Fatalf("exact receipt recovery failed: %d %s", status, data)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.OriginalText != text || detail.AITitle != "Keep title" || detail.Summary != "Keep summary" ||
		detail.TranslatedText != "Keep translation" || len(detail.Images) != 1 || detail.Images[0].Key != image {
		t.Fatalf("source save changed reading/media: %v %+v", err, detail)
	}
	reader := &localSourceReader{queue: queue}
	p := processor.NewStaged(queue, reader, classifier, catalog.Version, "jev-latest", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	p.SetPaidStageTimeout(10 * time.Second)
	if done, failed, err := p.RunClassifications(ctx, 1); err != nil || done != 1 || failed != 0 || modelCalls.Load() != 1 || reader.transforms != 0 {
		t.Fatalf("classification did not run independently: %d/%d/%d %v", done, failed, modelCalls.Load(), err)
	}
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("reading source is not claimable: %+v %v", job, err)
	}
	if err := p.Process(ctx, job); err != nil || reader.transforms != 1 || reader.fetches != 0 {
		t.Fatalf("reading did not reuse the exact checkpoint: %v", err)
	}
	if done, _, err := p.RunClassifications(ctx, 1); err != nil || done != 0 || modelCalls.Load() != 1 {
		t.Fatal("reading or source receipt created a duplicate classification")
	}
	status, data = send(nil)
	if status != 200 || !bytes.Contains(data, []byte(`"reading":{"state":"completed","output_state":"current"`)) ||
		!bytes.Contains(data, []byte(`"classification":{"state":"completed"`)) || json.Unmarshal(data, &view) != nil {
		t.Fatalf("independent lifecycle states were wrong: %d %s", status, data)
	}
	detail, err = queue.GetBookmark(ctx, id)
	if err != nil || detail.OriginalText != text || len(detail.Images) != 1 || detail.Images[0].Key != image {
		t.Fatal("reading destroyed original bytes or media")
	}
	effective, err := queue.GetV2Effective(ctx, id)
	if err != nil || !strings.Contains(string(effective), `"use":"contra"`) {
		t.Fatalf("human override changed: %s %v", effective, err)
	}
	retry := cairn.ProcessingIntent{Version: 1, OperationKey: "classification-only", Fence: view.Fence, Action: "retry_classification",
		ExpectedContentRevision: view.Revision, TargetGeneration: view.Generation}
	if status, data = send(&retry); status != 200 || !bytes.Contains(data, []byte(`"reading_queued":false`)) || reader.transforms != 1 || modelCalls.Load() != 1 {
		t.Fatalf("classification-only retry changed reading or called a model immediately: %d %s", status, data)
	}
	if done, failed, err := p.RunClassifications(ctx, 1); err != nil || done != 1 || failed != 0 || modelCalls.Load() != 2 || reader.transforms != 1 {
		t.Fatalf("explicit classification-only retry did not run exactly once: %d/%d %v", done, failed, err)
	}
	if status, data = send(&intent); status != 200 || !bytes.Contains(data, []byte(`"replayed":true`)) || modelCalls.Load() != 2 || reader.transforms != 1 {
		t.Fatal("late source receipt became another processing request")
	}
	t.Log("Actual Worker/D1/R2 + Go gateway + queue consumers: lost commit response recovered, primary/context/media/human retained, one independent reading and two explicitly requested contract classifications, no source fetch or live provider call")
}
