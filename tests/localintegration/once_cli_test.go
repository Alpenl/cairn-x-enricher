package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
		command := exec.CommandContext(ctx, binary, "serve") // #nosec G204 -- isolated binary and fixed arguments.
		command.Dir = work
		command.Env = append(append([]string(nil), commonEnv...), extra...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		err := command.Run()
		if err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil
	}
	if err := runServe(); err == nil || grokCalls.Load() != 0 {
		t.Fatalf("serve accepted a missing Grok key: err=%v grok_calls=%d", err, grokCalls.Load())
	}
	if err := runServe("GROK_MODELS_BASE_URL="+grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test"); err == nil || grokCalls.Load() != 1 {
		t.Fatalf("serve skipped empty-queue canary: err=%v grok_calls=%d", err, grokCalls.Load())
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
	if _, err := run("GROK_MODELS_BASE_URL="+grok.URL, "XAI_API_KEY=fixture", "GROK_MODEL=grok-test"); err == nil || grokCalls.Load() != 1 {
		t.Fatalf("source queue skipped or repeated failing canary: err=%v calls=%d", err, grokCalls.Load())
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Attempts != 0 || detail.Status != "pending" {
		t.Fatalf("canary failure consumed source lease: detail=%+v error=%v", detail, err)
	}
	t.Log("empty source queue: zero Grok calls and classification succeeds; pending source: missing key fails and one failing canary stops before lease")
}
