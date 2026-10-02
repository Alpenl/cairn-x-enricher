package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestLocalWorkerServeTargetSwitchWithManualSource(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec := mustSpec(t, catalog)
	if err := queue.PutQuestionSpec(ctx, spec); err != nil {
		t.Fatal(err)
	}
	upstream := providerContractServer(t, spec, "jev-1.13.0")
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	var modelCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		proxy.ServeHTTP(w, r)
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-1.13.0", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	var grokCalls atomic.Int64
	grok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grokCalls.Add(1)
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected reading path %q", r.URL.Path)
		}
		output := `{"ai_title":"契约自检通过的中文标题","original_language":"en","translated_text":"自检译文","summary":"自检摘要"}`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "grok-test", "output": []any{
			map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": output}}},
		}})
	}))
	defer grok.Close()
	reader := enrich.NewResponsesClient(grok.URL, "fixture", "grok-test", 8192, "fixture", grok.Client(), catalog)
	if _, err := reader.Transform(ctx, enrich.Input{URL: "https://x.com/canary/status/0", Attempt: 1,
		SourceText: "Canary check: validate structured reading aids.", Canary: true}); err != nil {
		t.Fatalf("local Grok canary fixture: %v", err)
	}
	grokCalls.Store(0)

	id := createLink(t, base, "app")
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.CacheIdentity == nil {
		t.Fatalf("read initial revision: %+v %v", detail.CacheIdentity, err)
	}
	const original = " A manually submitted source is classified after the target is restored. "
	if _, err := queue.SaveManualSource(ctx, id, fmt.Sprintf("serve-target-%d", id), detail.CacheIdentity.ContentRevision, original); err != nil {
		t.Fatal(err)
	}
	// Hold the independent reading lease so this test isolates classification.
	// The manual source is already durable and the classification snapshot can
	// be submitted without any reading-model request.
	_ = claimEnrichmentJob(t, base, "internal", id)
	source := enrich.Source{OriginalText: original, OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, "internal", classifier, "jev-other")
	before, err := queue.GetClassificationStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	work := t.TempDir()
	binary := filepath.Join(work, "cairn-x-enricher")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cairn-x-enricher") // #nosec G204 -- fixed package and isolated output.
	build.Dir = "../.."
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build serve CLI: %v %s", buildErr, output)
	}
	logFile, err := os.CreateTemp(work, "serve-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	command := exec.CommandContext(ctx, binary, "serve") // #nosec G204 -- isolated compiled CLI.
	command.Dir = work
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "CAIRN_API_BASE_URL=" + base, "CAIRN_ENRICHER_TOKEN=internal",
		"TYPESAFE_BASE_URL=" + provider.URL, "TYPESAFE_API_KEY=fixture", "TYPESAFE_MODEL=jev-1.13.0",
		"GROK_MODELS_BASE_URL=" + grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test",
		"HTTP_ADDR=" + address, "POLL_INTERVAL=200ms", "MAX_JOBS_PER_RUN=1", "SHUTDOWN_TIMEOUT=2s", "LOG_LEVEL=error"}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-exited:
		default:
		}
	})
	healthURL := "http://" + address + "/healthz"
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, getErr := http.DefaultClient.Do(request)
		if getErr == nil {
			_ = response.Body.Close()
			ready = response.StatusCode == http.StatusOK
		}
		if ready {
			break
		}
		select {
		case exitErr := <-exited:
			logBytes, _ := os.ReadFile(logFile.Name())
			t.Fatalf("serve exited before health: %v %s", exitErr, logBytes)
		default:
		}
	}
	if !ready {
		t.Fatal("serve health endpoint did not start")
	}
	time.Sleep(650 * time.Millisecond)
	after, err := queue.GetClassificationStatus(ctx, id)
	if err != nil || string(after) != string(before) || modelCalls.Load() != 0 {
		t.Fatalf("incompatible target changed classification job or called model: before=%s after=%s calls=%d error=%v", before, after, modelCalls.Load(), err)
	}
	switchTarget(t, base, "internal", classifier, "jev-1.13.0")
	var run *cairn.StoredRun
	// A configuration mismatch opens the component breaker for 30 seconds.
	// Restoring the target must not bypass that backoff; the next scheduled
	// half-open probe should then complete the pending manual source.
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		run, err = queue.GetLatestRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil {
			break
		}
	}
	batchCount := int64((len(spec.Questions) + classify.DefaultMaxQuestionsPerRequest - 1) / classify.DefaultMaxQuestionsPerRequest)
	if run == nil || run.SpecID != spec.SpecID || run.SpecHash != spec.SemanticHash || modelCalls.Load() != batchCount || grokCalls.Load() != 1 {
		logBytes, _ := os.ReadFile(logFile.Name())
		t.Fatalf("serve did not resume exact manual source: run=%+v model_calls=%d grok_calls=%d logs=%s", run, modelCalls.Load(), grokCalls.Load(), logBytes)
	}
	stored, err := queue.GetSource(ctx, id)
	if err != nil || stored == nil || stored.OriginalText != original {
		t.Fatalf("manual source changed during serve target switch: source=%+v error=%v", stored, err)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case exitErr := <-exited:
		if exitErr != nil {
			t.Fatalf("serve shutdown: %v", exitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after SIGTERM")
	}
	t.Log("compiled serve: manual source durable, incompatible target did not lease or call TypeSafe, restored target classified once; one startup Grok canary")
}
