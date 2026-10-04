package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
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

// The compiled one-shot command uses a real local Worker/D1. The provider
// fixtures count calls but never forward them to a paid endpoint.
func TestLocalWorkerOnceSkipsEmptySourceCanary(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grokCalls atomic.Int64
	grok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		grokCalls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"canary rejected"}`))
	}))
	defer grok.Close()
	typesafe := providerContractServer(t, mustSpec(t, catalog), "jev-1.13.0")
	defer typesafe.Close()
	classifier, err := classify.NewClient(typesafe.URL, "fixture", "jev-1.13.0", typesafe.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, "internal", classifier, "jev-1.13.0")
	work := t.TempDir()
	binary := filepath.Join(work, "cairn-x-enricher")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cairn-x-enricher") // #nosec G204 -- fixed package and isolated output.
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build once CLI: %v %s", err, output)
	}
	commonEnv := []string{"PATH=" + os.Getenv("PATH"), "CAIRN_API_BASE_URL=" + base,
		"CAIRN_ENRICHER_TOKEN=internal", "TYPESAFE_BASE_URL=" + typesafe.URL,
		"TYPESAFE_API_KEY=fixture", "TYPESAFE_MODEL=jev-1.13.0", "LOG_LEVEL=error"}
	run := func(extra ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, "once", "--max-jobs", "1") // #nosec G204 -- isolated binary and fixed arguments.
		command.Dir = work
		command.Env = append(append([]string(nil), commonEnv...), extra...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if err != nil {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, stderr.String())
		}
		return stdout.Bytes(), nil
	}
	runServe := func(extra ...string) error {
		t.Helper()
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			return err
		}
		command := exec.CommandContext(ctx, binary, "serve") // #nosec G204 -- isolated binary and fixed arguments.
		command.Dir = work
		command.Env = append(append([]string(nil), commonEnv...), "HTTP_ADDR="+address)
		command.Env = append(command.Env, extra...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if len(extra) == 0 {
			if err := command.Run(); err != nil {
				return fmt.Errorf("%w: %s", err, stderr.String())
			}
			return nil
		}
		if err := command.Start(); err != nil {
			return err
		}
		defer func() { _ = command.Process.Kill() }()
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(8 * time.Second)
		var ready bool
		for time.Now().Before(deadline) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/readyz", nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err == nil {
				var status struct {
					Ready  bool   `json:"ready"`
					Reason string `json:"ready_reason"`
				}
				decodeErr := json.NewDecoder(response.Body).Decode(&status)
				_ = response.Body.Close()
				if decodeErr == nil && !status.Ready && strings.Contains(status.Reason, "source provider contract check") && grokCalls.Load() == 1 {
					ready = true
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !ready {
			_ = command.Process.Kill()
			_ = command.Wait()
			return fmt.Errorf("Reader did not remain reachable with paused source: %s", stderr.String())
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/bookmarks?view=summary", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("Reader HTTP %d under source outage", response.StatusCode)
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			return err
		}
		if err := command.Wait(); err != nil {
			return fmt.Errorf("shutdown: %w: %s", err, stderr.String())
		}
		return nil
	}
	if err := runServe(); err == nil || grokCalls.Load() != 0 {
		t.Fatalf("serve accepted a missing Grok key: err=%v grok_calls=%d", err, grokCalls.Load())
	}
	if err := runServe("GROK_MODELS_BASE_URL="+grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test"); err != nil || grokCalls.Load() != 1 {
		t.Fatalf("serve outage blocked Reader or skipped deferred canary: err=%v grok_calls=%d", err, grokCalls.Load())
	}
	grokCalls.Store(0)
	stdout, err := run()
	if err != nil {
		t.Fatalf("empty once with no Grok credential: %v", err)
	}
	var stats struct {
		Claimed    int64 `json:"claimed"`
		Classified int64 `json:"classified"`
	}
	if err := json.Unmarshal(stdout, &stats); err != nil || stats.Claimed != 0 || stats.Classified != 0 || grokCalls.Load() != 0 {
		t.Fatalf("empty once: stats=%s grok_calls=%d error=%v", stdout, grokCalls.Load(), err)
	}
	// Classification of a saved source remains available without Grok.
	classificationID := createLink(t, base, "app")
	lease := claimEnrichmentJob(t, base, "internal", classificationID)
	source := enrich.Source{OriginalText: "A saved source for classification without Grok.",
		OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, classificationID, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, classificationID, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	stdout, err = run()
	if err != nil || json.Unmarshal(stdout, &stats) != nil || stats.Classified != 1 || grokCalls.Load() != 0 {
		t.Fatalf("classification-only once: stats=%s grok_calls=%d error=%v", stdout, grokCalls.Load(), err)
	}
	if stored, err := queue.GetLatestRun(ctx, classificationID); err != nil || stored == nil {
		t.Fatalf("classification-only once did not persist a run: %v %v", stored, err)
	}
	// The same entrypoint must pause classification on an incompatible target
	// without consuming the saved source's classification attempt.
	mismatchID := createLink(t, base, "app")
	mismatchLease := claimEnrichmentJob(t, base, "internal", mismatchID)
	if err := queue.SaveSource(ctx, mismatchID, mismatchLease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, mismatchID, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, "internal", classifier, "jev-other")
	before, err := queue.GetClassificationStatus(ctx, mismatchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(); err == nil {
		t.Fatal("once accepted incompatible classification target")
	}
	after, err := queue.GetClassificationStatus(ctx, mismatchID)
	if err != nil || !bytes.Equal(before, after) || grokCalls.Load() != 0 {
		t.Fatalf("once mismatch mutated job or called Grok: before=%s after=%s calls=%d error=%v", before, after, grokCalls.Load(), err)
	}
	switchTarget(t, base, "internal", classifier, "jev-1.13.0")
	id := createLink(t, base, "app")
	claimable, err := queue.SourceClaimable(ctx)
	if err != nil || !claimable {
		t.Fatalf("pending source not claimable: %t %v", claimable, err)
	}
	if _, err := run(); err == nil || grokCalls.Load() != 0 {
		t.Fatalf("source queue accepted a missing Grok credential: err=%v calls=%d", err, grokCalls.Load())
	}
	if _, err := run("GROK_MODELS_BASE_URL="+grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test"); err == nil || grokCalls.Load() != 0 {
		t.Fatalf("restart ignored durable check cooldown: err=%v calls=%d", err, grokCalls.Load())
	}
	scope := enrich.NewResponsesClient(grok.URL, "fixture", "grok-test", 8192, "fixture", grok.Client(), catalog).ReadingCheckScope()
	check, err := queue.ProviderCheck(ctx, scope, "status", nil)
	if err != nil || check.State != "waiting" || check.Failures != 1 || check.NextCheckAt <= time.Now().UnixMilli() {
		t.Fatalf("missing persisted check: %+v %v", check, err)
	}
	recovery, err := queue.ProviderCheck(ctx, scope, "recover", nil)
	if err != nil || recovery.Accepted {
		t.Fatalf("manual recovery ignored cooldown: %+v %v", recovery, err)
	}
	// Only this isolated fixture database advances the safety clock. Real
	// deployment verification never clears budgets or forces a paid probe.
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("missing isolated D1 configuration")
	}
	//nolint:gosec // Harness-owned local executable/config and constant synthetic SQL.
	advance := exec.CommandContext(ctx, filepath.Join(shareRoot, "worker/node_modules/.bin/wrangler"), "d1", "execute", "cairn-share-onceempty", "--local", "--config", configPath, "--command",
		"UPDATE provider_checks SET manual_after=0; UPDATE enrichment_provider_attempts SET created_at='2000-01-01T00:00:00.000Z' WHERE stage='canary';")
	advance.Dir = filepath.Join(shareRoot, "worker")
	if output, err := advance.CombinedOutput(); err != nil {
		t.Fatalf("advance isolated safety clock: %v %s", err, output)
	}
	recovery, err = queue.ProviderCheck(ctx, scope, "recover", nil)
	if err != nil || !recovery.Accepted || recovery.State != "pending" {
		t.Fatalf("manual recovery not queued: %+v %v", recovery, err)
	}
	if _, err := run("GROK_MODELS_BASE_URL="+grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test"); err == nil || grokCalls.Load() != 1 {
		t.Fatalf("manual recovery did not make exactly one fixture check: %v calls=%d", err, grokCalls.Load())
	}
	check, err = queue.ProviderCheck(ctx, scope, "status", nil)
	if err != nil || check.State != "waiting" || check.Failures != 2 || check.NextCheckAt < time.Now().Add(9*time.Minute).UnixMilli() {
		t.Fatalf("failed recovery lost increasing backoff: %+v %v", check, err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Attempts != 0 || detail.Status != "pending" {
		t.Fatalf("canary failure consumed source lease: detail=%+v error=%v", detail, err)
	}
	t.Log("empty source queue/classification need no Grok; restart preserves cooldown; admitted manual recovery makes one failed fixture check, increases backoff, and never consumes source leases")
}
