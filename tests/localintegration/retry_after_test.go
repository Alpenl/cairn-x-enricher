package localintegration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerProviderRetryHintSurvivesProcessorRestart(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	catalog := taxonomy.Catalog{Version: "2026-09-20.1",
		Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language model engineering", Active: true}},
		Forms:  []taxonomy.Term{{ID: "method", Label: "Method", Description: "Procedure", Active: true}},
		Uses:   []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try later", Active: true}}}
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("retry-after-ms", "480000")
		w.WriteHeader(529)
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "A synthetic guide to model evaluation.", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	// A second eligible bookmark proves that the pause is component-wide, not
	// merely the failed job's persisted next_retry_at.
	created := postJSON(ctx, t, base+"/api/links", envOr("CAIRN_APP_TOKEN", "app"),
		map[string]any{"url": "https://x.com/local/status/2", "note": ""})
	secondID := int64(created["id"].(float64))
	secondLease := claimEnrichmentJob(t, base, token, secondID)
	if err := queue.SaveSource(ctx, secondID, secondLease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, secondID, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest", logger, 1)
	started := time.Now()
	done, failed, err := first.RunClassifications(ctx, 5)
	if !errors.Is(err, processor.ErrComponentPaused) || done != 0 || failed != 1 || calls.Load() != 1 {
		t.Fatalf("initial overload: done=%d failed=%d calls=%d err=%v", done, failed, calls.Load(), err)
	}
	if paused, _, remaining := first.ClassificationPaused(); !paused || remaining < 8*time.Minute-time.Second {
		t.Fatalf("local pause did not honor provider cooldown: paused=%t remaining=%s", paused, remaining)
	}
	raw, err := queue.GetClassificationStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Status      string `json:"status"`
		Attempts    int    `json:"attempts"`
		NextRetryAt string `json:"next_retry_at"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	retryAt, err := time.Parse(time.RFC3339Nano, state.NextRetryAt)
	if err != nil || state.Status != "failed" || state.Attempts != 1 || retryAt.Sub(started) < 8*time.Minute {
		t.Fatalf("durable retry hint = %+v, parsed=%v, err=%v", state, retryAt, err)
	}
	// A new Go processor has no local pause state; the Worker must also refuse
	// the *other* eligible job before the shared provider cooldown ends.
	restarted := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest", logger, 1)
	done, failed, err = restarted.RunClassifications(ctx, 1)
	if !errors.Is(err, processor.ErrComponentPaused) || done != 0 || failed != 0 || calls.Load() != 1 {
		t.Fatalf("restart ignored durable retry: done=%d failed=%d calls=%d err=%v", done, failed, calls.Load(), err)
	}
	secondRaw, err := queue.GetClassificationStatus(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	var secondState struct {
		Attempts int `json:"attempts"`
	}
	if err := json.Unmarshal(secondRaw, &secondState); err != nil || secondState.Attempts != 0 {
		t.Fatalf("shared cooldown consumed the other job: %+v %v", secondState, err)
	}
	t.Log("real Worker/D1 persisted TypeSafe 529 cooldown across a new Go processor and another eligible job; one local provider call")
}
