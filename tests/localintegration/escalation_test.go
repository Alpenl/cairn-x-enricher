package localintegration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerEvidenceExecutionRecovery(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language models", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Description: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try", Active: true}}}
	ordinary := providerContractServer(t, mustSpec(t, catalog))
	defer ordinary.Close()
	var modelCalls, fetchCalls, checkpointCalls, finalizeCalls atomic.Int64
	var statesMu sync.Mutex
	var states []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var body struct {
			State json.RawMessage `json:"state"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		modelCalls.Add(1)
		statesMu.Lock()
		states = append(states, string(body.State))
		statesMu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		ordinary.Config.Handler.ServeHTTP(w, r)
	}))
	defer provider.Close()
	fetchStarted := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	const newText = "Unique external article text about model reliability."
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("internal token escaped to external fetch")
		}
		startOnce.Do(func() { close(fetchStarted) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<article>"+newText+"</article>")
	}))
	defer external.Close()
	externalURL, _ := url.Parse(external.URL)
	fetcher := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "allowed.example" {
			return nil, fmt.Errorf("unexpected external host")
		}
		cloned := r.Clone(r.Context())
		copied := *r.URL
		copied.Scheme = externalURL.Scheme
		copied.Host = externalURL.Host
		cloned.URL = &copied
		return http.DefaultTransport.RoundTrip(cloned)
	})}
	policy := extension.DefaultFetchPolicy([]string{"allowed.example"})
	var logs strings.Builder
	flags := extension.DefaultFlags()
	flags.Evidence = true
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	const storedURL = "https://allowed.example/article"
	source := enrich.Source{OriginalText: "Primary model engineering source.", OriginalLanguage: "en", ContextText: "Stored context", RelatedLinks: []string{storedURL}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	blocks := []map[string]any{{"id": "primary-custom-900", "role": "primary", "text": source.OriginalText, "acquired": "fetch"}, {"id": "quoted-870", "role": "quoted", "text": "Quoted independent position", "relation": "quoted author"}, {"id": "continuation-42", "role": "author_continuation", "text": "Original continuation", "relation": "same author"}}
	snapshot := map[string]any{"blocks": blocks, "retrieval": "archive", "fetched_at": "2026-09-22T00:00:00Z", "truncation": map[string]any{"truncated": false}}
	if err := queue.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	originalArchive, err := queue.GetEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Snapshot struct {
			Blocks []map[string]any `json:"blocks"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(originalArchive, &original); err != nil {
		t.Fatal(err)
	}
	// Real checkpoint commits but its first response is lost; finalize is then
	// unavailable for all bounded retries. A fresh processor must recover later.
	broken := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/finalize") {
			finalizeCalls.Add(1)
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"fixture finalize unavailable"}`)), Request: r}, nil
		}
		response, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/checkpoint") && checkpointCalls.Add(1) == 1 {
			if response.StatusCode != http.StatusOK {
				t.Errorf("real checkpoint before response loss returned %d", response.StatusCode)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			response.StatusCode = 503
			response.Body = io.NopCloser(strings.NewReader(`{"error":"fixture lost checkpoint response"}`))
		}
		return response, nil
	})})
	makeProcessor := func(q *cairn.Client) *processor.Processor {
		p := processor.NewStaged(q, nil, classifier, catalog.Version, "jev-latest", slog.New(slog.NewTextHandler(&logs, nil)), 1)
		p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), nil), fetcher, policy)
		return p
	}
	type outcome struct {
		done, failed int64
		err          error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		done, failed, err := makeProcessor(broken).RunClassifications(ctx, 1)
		firstDone <- outcome{done, failed, err}
	}()
	select {
	case <-fetchStarted:
	case <-ctx.Done():
		t.Fatal("external fetch never started: " + logs.String())
	}
	run, err := queue.GetLatestRun(ctx, id)
	if err != nil || run == nil {
		t.Fatalf("initial run: %v", err)
	}
	urlHash := fmt.Sprintf("%x", sha256.Sum256([]byte(storedURL)))[:24]
	ack, err := queue.CreateEvidenceRequest(ctx, id, map[string]any{"protocol": 1, "scope": "external_link", "dedupe_key": fmt.Sprintf("evidence-%d-%d-%s", id, run.EvidenceSnapshotID, urlHash), "url": storedURL,
		"evidence_snapshot_id": run.EvidenceSnapshotID, "source_hash": run.SourceHash, "content_revision": run.ContentRevision, "target_generation": run.TargetGeneration, "budget": map[string]any{"max_bytes": policy.MaxBytes, "timeout_ms": policy.Timeout.Milliseconds()}})
	if err != nil || !ack.Replayed || ack.Status != "fetching" {
		t.Fatalf("strict replay response: %+v %v", ack, err)
	}
	other, err := queue.ClaimEvidenceRequest(ctx, ack.ID, "competing-owner")
	if err != nil || other.Owned || other.OwnerToken != nil || other.Attempts != 1 {
		t.Fatalf("pending duplicate acquired fetch: %+v %v", other, err)
	}
	// A second normal poll also sees no executable pending owner.
	done, failed, err := makeProcessor(queue).RunClassifications(ctx, 1)
	if err != nil || done != 0 || failed != 0 || fetchCalls.Load() != 1 {
		t.Fatalf("competing processor fetched: %d/%d %v fetch=%d", done, failed, err, fetchCalls.Load())
	}
	releaseOnce.Do(func() { close(release) })
	first := <-firstDone
	if first.err != nil || first.done != 1 || first.failed != 0 {
		t.Fatalf("extension fault failed original classification: %+v logs=%s", first, logs.String())
	}
	if checkpointCalls.Load() != 2 || finalizeCalls.Load() != 3 || modelCalls.Load() != 1 || fetchCalls.Load() != 1 {
		t.Fatalf("not bounded/replay-safe: checkpoint=%d finalize=%d model=%d fetch=%d logs=%s", checkpointCalls.Load(), finalizeCalls.Load(), modelCalls.Load(), fetchCalls.Load(), logs.String())
	}
	pending, err := queue.RecoverableEvidenceRequests(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Status != "checkpointed" || pending[0].Attempts != 1 {
		t.Fatalf("durable recovery missing: %+v %v", pending, err)
	}
	// New processor, no in-memory fetch outcome: finalize stored text and classify
	// the appended snapshot in the same bounded poll.
	recovered := makeProcessor(queue)
	done, failed, err = recovered.RunClassifications(ctx, 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("checkpoint recovery %d/%d %v logs=%s", done, failed, err, logs.String())
	}
	if fetchCalls.Load() != 1 || modelCalls.Load() != 2 {
		t.Fatal("recovery repeated fetch or inference")
	}
	statesMu.Lock()
	captured := append([]string(nil), states...)
	statesMu.Unlock()
	if len(captured) != 2 || strings.Contains(captured[0], newText) || !strings.Contains(captured[1], newText) || !strings.Contains(captured[1], "external_article") {
		t.Fatalf("actual provider state did not gain external evidence: %v", captured)
	}
	saved, err := queue.GetEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var current struct {
		Snapshot struct {
			Blocks []map[string]any `json:"blocks"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(saved, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Snapshot.Blocks) != 4 || !reflect.DeepEqual(current.Snapshot.Blocks[:3], original.Snapshot.Blocks) {
		t.Fatalf("archived blocks changed: %+v", current.Snapshot.Blocks)
	}
	before, err := queue.GetClassificationStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	done, failed, err = recovered.RunClassifications(ctx, 1)
	after, afterErr := queue.GetClassificationStatus(ctx, id)
	if err != nil || afterErr != nil || done != 0 || failed != 0 || fetchCalls.Load() != 1 || modelCalls.Load() != 2 || string(before) != string(after) {
		t.Fatalf("third unchanged poll looped: %d/%d %v %v", done, failed, err, afterErr)
	}
	requests, err := queue.RecoverableEvidenceRequests(ctx, 10)
	if err != nil || len(requests) != 0 {
		t.Fatal("completed request remains pending")
	}
	t.Log("real production evidence escalation: competing owner denied; 1 external HTTP fetch, checkpoint lost-response replay, 3 bounded finalize failures, new processor recovery, 2 model fixture calls; 3 original blocks retained and third poll unchanged")
}
