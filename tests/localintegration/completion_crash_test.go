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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// The child runs the real staged processor. It exits after a successful D1
// commit but before its Go client reads the completion acknowledgment. A new
// process then confirms that exact operation and sees no classification work.
func TestLocalWorkerClassificationCompletionSurvivesProcessExit(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	appToken := envOr("CAIRN_APP_TOKEN", "app")
	enricherToken := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	catalog := completionRaceCatalog()
	ordinary := providerContractServer(t, mustSpec(t, catalog))
	defer ordinary.Close()
	var modelCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		modelCalls.Add(1)
		ordinary.Config.Handler.ServeHTTP(writer, request)
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "local-key", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	queue := cairn.NewClient(base, enricherToken, &http.Client{Timeout: 30 * time.Second})
	id := createLink(t, base, appToken)
	lease := claimEnrichmentJob(t, base, enricherToken, id)
	source := enrich.Source{OriginalText: "A reproducible guide to evaluating language models.",
		OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatalf("save source: %v", err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatalf("submit evidence: %v", err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatalf("register spec: %v", err)
	}
	switchTarget(t, base, enricherToken, classifier)
	payloadPath := filepath.Join(t.TempDir(), "completion.json")
	//nolint:gosec // Re-executes this fixed test binary with local fixture values.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerClassificationCompletionCrashHelper$")
	child.Env = append(os.Environ(), "CAIRN_COMPLETION_CRASH_HELPER=1",
		"CAIRN_COMPLETION_CRASH_PROVIDER="+provider.URL,
		"CAIRN_COMPLETION_CRASH_PAYLOAD="+payloadPath)
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "classification committed before process exit") {
		t.Fatalf("completion child exit=%v output=%s", err, output)
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("child performed %d model calls, want 1", modelCalls.Load())
	}
	//nolint:gosec // payloadPath is created under this test's private TempDir.
	payload, err := os.ReadFile(payloadPath)
	if err != nil || len(payload) == 0 {
		t.Fatalf("read exact completion request: bytes=%d err=%v", len(payload), err)
	}

	// A newly constructed client has no in-memory operation or lease state.
	restarted := cairn.NewClient(base, enricherToken, &http.Client{Timeout: 30 * time.Second})
	if err := restarted.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatalf("register restarted spec: %v", err)
	}
	runs, err := restarted.GetRuns(ctx, id)
	if err != nil || len(runs) != 1 {
		t.Fatalf("stored runs after child exit=%d err=%v", len(runs), err)
	}
	decision, err := restarted.GetLatestDecision(ctx, id)
	if err != nil || decision == nil {
		t.Fatalf("stored decision after child exit=%+v err=%v", decision, err)
	}
	next, err := restarted.ClaimClassification(ctx, classifier.SpecID(), catalog.Version, "jev-latest")
	if err != nil || next != nil {
		t.Fatalf("restarted process reclaimed committed job=%+v err=%v", next, err)
	}
	type completionReply struct {
		status int
		code   string
		err    error
	}
	sendCompletion := func(body []byte) completionReply {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			fmt.Sprintf("%s/api/enrichment/classifications/%d/complete", base, id), bytes.NewReader(body))
		if err != nil {
			return completionReply{err: err}
		}
		request.Header.Set("Authorization", "Bearer "+enricherToken)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Cairn-Classification-Budget", "1")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return completionReply{err: err}
		}
		defer func() { _ = response.Body.Close() }()
		var result struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			return completionReply{status: response.StatusCode, err: err}
		}
		return completionReply{status: response.StatusCode, code: result.Error}
	}
	if replay := sendCompletion(payload); replay.err != nil || replay.status != http.StatusOK {
		t.Fatalf("exact replay after process exit: %+v", replay)
	}
	parallel := make(chan completionReply, 2)
	for range 2 {
		go func() { parallel <- sendCompletion(payload) }()
	}
	for range 2 {
		if replay := <-parallel; replay.err != nil || replay.status != http.StatusOK {
			t.Fatalf("concurrent exact replay after process exit: %+v", replay)
		}
	}
	var changed map[string]any
	if err := json.Unmarshal(payload, &changed); err != nil {
		t.Fatal(err)
	}
	result, ok := changed["result"].(map[string]any)
	if !ok {
		t.Fatal("serialized completion has no result")
	}
	classification, ok := result["classification"].(map[string]any)
	if !ok {
		t.Fatal("serialized completion has no classification")
	}
	originalWhy := classification["why_suggestion"]
	classification["why_suggestion"] = "changed after the first commit"
	changedBody, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if conflict := sendCompletion(changedBody); conflict.err != nil ||
		conflict.status != http.StatusConflict || conflict.code != "operation_conflict" {
		t.Fatalf("same key with changed result: %+v", conflict)
	}
	classification["why_suggestion"] = originalWhy
	changed["operation_key"] = fmt.Sprintf("classification-other-%d", id)
	otherBody, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if conflict := sendCompletion(otherBody); conflict.err != nil || conflict.status != http.StatusConflict {
		t.Fatalf("different key after completed job: %+v", conflict)
	}
	runs, err = restarted.GetRuns(ctx, id)
	if err != nil || len(runs) != 1 || modelCalls.Load() != 1 {
		t.Fatalf("replay duplicated work: runs=%d model=%d err=%v", len(runs), modelCalls.Load(), err)
	}
}

func TestLocalWorkerClassificationCompletionCrashHelper(t *testing.T) {
	if os.Getenv("CAIRN_COMPLETION_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	catalog := completionRaceCatalog()
	classifier, err := classify.NewClient(os.Getenv("CAIRN_COMPLETION_CRASH_PROVIDER"), "local-key",
		"jev-latest", &http.Client{Timeout: 30 * time.Second}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/complete") {
			return http.DefaultTransport.RoundTrip(request)
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
		//nolint:gosec // The parent passes a private TempDir path to this fixed child test.
		if err := os.WriteFile(os.Getenv("CAIRN_COMPLETION_CRASH_PAYLOAD"), payload, 0o600); err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(payload))
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil || response.StatusCode != http.StatusOK {
			return response, err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		_, _ = os.Stdout.WriteString("classification committed before process exit\n")
		os.Exit(0)
		return nil, nil
	})}
	queue := cairn.NewClient(workerURL(t), envOr("CAIRN_ENRICHER_TOKEN", "internal"), client)
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	p := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest",
		slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	done, failed, err := p.RunClassifications(ctx, 1)
	t.Fatalf("processor returned without injected exit: done=%d failed=%d err=%v", done, failed, err)
}
