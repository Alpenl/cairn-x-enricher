package localintegration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Only the classification claim is faulted. Handshakes, source and reading
// operations still cross the real Worker HTTP/D1 boundary.
type halfOpenClaimTransport struct {
	mu     sync.Mutex
	mode   string
	claims int
}

func (f *halfOpenClaimTransport) setMode(mode string) {
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
}

func (f *halfOpenClaimTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func (f *halfOpenClaimTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/classifications/claim") {
		return http.DefaultTransport.RoundTrip(request)
	}
	f.mu.Lock()
	f.claims++
	mode := f.mode
	f.mu.Unlock()
	switch mode {
	case "401", "503":
		status := http.StatusUnauthorized
		code := "configuration_error"
		if mode == "503" {
			status = http.StatusServiceUnavailable
			code = "upstream_unavailable"
		}
		body, _ := json.Marshal(map[string]string{"error": code})
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	case "network":
		return nil, io.ErrUnexpectedEOF
	default:
		return http.DefaultTransport.RoundTrip(request)
	}
}

func waitForHalfOpen(ctx context.Context, t *testing.T, processors ...*processor.Processor) {
	t.Helper()
	var wait time.Duration
	for _, p := range processors {
		paused, _, remaining := p.ClassificationPaused()
		if !paused || remaining <= 0 {
			t.Fatalf("classification is not waiting for a bounded probe: paused=%t remaining=%s", paused, remaining)
		}
		if remaining > wait {
			wait = remaining
		}
	}
	timer := time.NewTimer(wait + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatalf("deadline before half-open probe: %v", ctx.Err())
	}
}

func TestLocalWorkerHalfOpenClaimFaultsAndIndependentSource(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	appToken := envOr("CAIRN_APP_TOKEN", "app")
	catalog := taxonomy.Catalog{Version: "2026-09-20.1",
		Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language model engineering", Active: true}},
		Forms:  []taxonomy.Term{{ID: "method", Label: "Method", Description: "Procedure", Active: true}},
		Uses:   []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try later", Active: true}}}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var modelCalls atomic.Int64
	providerHTTP := provider.Client()
	providerTransport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		modelCalls.Add(1)
		return providerTransport.RoundTrip(request)
	})
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", providerHTTP, catalog)
	if err != nil {
		t.Fatal(err)
	}
	firstFault := &halfOpenClaimTransport{mode: "401"}
	secondFault := &halfOpenClaimTransport{mode: "401"}
	firstQueue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second, Transport: firstFault})
	secondQueue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second, Transport: secondFault})
	if err := firstQueue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	if err := secondQueue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)

	// The first saved source stays eligible for classification while its source
	// lease is open. The second link is then processed during the model pause.
	firstID := createLink(t, base, appToken)
	lease := claimEnrichmentJob(t, base, token, firstID)
	source := enrich.Source{OriginalText: "A synthetic guide to language model evaluation.", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := firstQueue.SaveSource(ctx, firstID, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := firstQueue.SubmitEvidence(ctx, firstID, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	created := postJSON(ctx, t, base+"/api/links", appToken,
		map[string]any{"url": "https://x.com/halfopen/status/2", "note": ""})
	secondID := int64(created["id"].(float64))
	seedArchivedOriginal(ctx, t, secondID, "Fixture original text")
	if secondID == firstID {
		t.Fatal("fixture links were not distinct")
	}
	reader := &localSourceReader{queue: firstQueue}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := processor.NewStaged(firstQueue, reader, classifier, catalog.Version, "jev-latest", logger, 1)
	second := processor.NewStaged(secondQueue, nil, classifier, catalog.Version, "jev-latest", logger, 1)
	var initialFaults []error
	for _, p := range []*processor.Processor{first, second} {
		if _, _, err := p.RunClassifications(ctx, 5); !errors.Is(err, processor.ErrComponentPaused) {
			t.Fatalf("HTTP 401 did not pause classification: %v", err)
		} else {
			initialFaults = append(initialFaults, err)
		}
	}
	if firstFault.count() != 1 || secondFault.count() != 1 || modelCalls.Load() != 0 {
		t.Fatalf("initial fault consumed work: claims=%d/%d model=%d errors=%v", firstFault.count(), secondFault.count(), modelCalls.Load(), initialFaults)
	}
	for _, p := range []*processor.Processor{first, second} {
		if _, _, err := p.RunClassifications(ctx, 5); !errors.Is(err, processor.ErrComponentPaused) {
			t.Fatalf("backoff did not suppress claim: %v", err)
		}
	}
	if firstFault.count() != 1 || secondFault.count() != 1 {
		t.Fatal("backoff reached the classification claim endpoint")
	}
	stats, err := first.RunSources(ctx, 1)
	if err != nil || stats.Completed != 1 || reader.fetches != 0 || reader.transforms != 1 {
		t.Fatalf("classification pause blocked real source/reading: stats=%+v fetches=%d reading=%d err=%v",
			stats, reader.fetches, reader.transforms, err)
	}
	if detail, err := firstQueue.GetBookmark(ctx, secondID); err != nil || detail.Status != "completed" {
		t.Fatalf("source/reading completion was not persisted: %+v %v", detail, err)
	}
	if modelCalls.Load() != 0 {
		t.Fatal("source/reading work invoked the classifier")
	}

	waitForHalfOpen(ctx, t, first, second)
	firstFault.setMode("503")
	secondFault.setMode("network")
	for _, p := range []*processor.Processor{first, second} {
		if _, _, err := p.RunClassifications(ctx, 5); err == nil {
			t.Fatal("failed half-open claim reported success")
		}
		if paused, _, remaining := p.ClassificationPaused(); !paused || remaining <= 30*time.Second {
			t.Fatalf("failed probe did not extend backoff: paused=%t remaining=%s", paused, remaining)
		}
	}
	if firstFault.count() != 2 || secondFault.count() != 2 || modelCalls.Load() != 0 {
		t.Fatalf("failed probes claimed extra work: claims=%d/%d model=%d", firstFault.count(), secondFault.count(), modelCalls.Load())
	}
	waitForHalfOpen(ctx, t, first, second)
	firstFault.setMode("")
	secondFault.setMode("")
	type outcome struct {
		done, failed int64
		err          error
	}
	results := make(chan outcome, 2)
	for _, p := range []*processor.Processor{first, second} {
		go func(p *processor.Processor) {
			done, failed, err := p.RunClassifications(ctx, 5)
			results <- outcome{done: done, failed: failed, err: err}
		}(p)
	}
	for range 2 {
		result := <-results
		if result.err != nil || result.done != 1 || result.failed != 0 {
			t.Fatalf("bounded recovery failed: %+v", result)
		}
	}
	for _, p := range []*processor.Processor{first, second} {
		if paused, _, _ := p.ClassificationPaused(); paused {
			t.Fatal("successful model result did not clear classification pause")
		}
	}
	if firstFault.count() != 3 || secondFault.count() != 3 || modelCalls.Load() != 2 {
		t.Fatalf("recovery was not one claim and model call per processor: claims=%d/%d model=%d",
			firstFault.count(), secondFault.count(), modelCalls.Load())
	}
	for _, id := range []int64{firstID, secondID} {
		if run, err := firstQueue.GetLatestRun(ctx, id); err != nil || run == nil {
			t.Fatalf("classification run %d was not persisted: %+v %v", id, run, err)
		}
	}
	t.Log("real Worker/D1 and Go client: 401 pause, 503/network half-open failures, source/reading completion during pause, one bounded recovery each; 2 local model fixture calls")
}
