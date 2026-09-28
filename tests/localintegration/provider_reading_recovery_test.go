package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// This uses the real Go Responses adapter and Worker/D1 over HTTP. The local
// provider fixture stores its result; only the external provider is simulated.
func TestLocalWorkerProviderReadingRecovery(t *testing.T) {
	base := workerURL(t)
	configPath := os.Getenv("CAIRN_WRANGLER_CONFIG")
	shareRoot := os.Getenv("CAIRN_SHARE_ROOT")
	if configPath == "" || shareRoot == "" {
		t.Fatal("real D1 lease expiry requires CAIRN_WRANGLER_CONFIG and CAIRN_SHARE_ROOT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	queue := cairn.NewClient(base, "internal", httpClient)
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	var posts, gets atomic.Int32
	const responseID = "resp_reading_local"
	providerBody := map[string]any{
		"object": "response", "id": responseID, "status": "completed", "model": "grok-test",
		"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{
			"type": "output_text", "text": `{"ai_title":"用于验证阅读恢复的中文标题","original_language":"en","translated_text":"夹具译文","summary":"夹具摘要"}`}}}},
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120,
			"cost_in_usd_ticks": 1234},
	}
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			posts.Add(1)
			if request.URL.Path != "/responses" {
				t.Errorf("provider POST path %s", request.URL.Path)
			}
		case http.MethodGet:
			gets.Add(1)
			if request.URL.Path != "/responses/"+responseID {
				t.Errorf("provider GET path %s", request.URL.Path)
			}
		default:
			t.Errorf("unexpected provider method %s", request.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(providerBody)
	}))
	defer provider.Close()
	id := createLink(t, base, "app")
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	source := enrich.Source{OriginalText: "Persisted primary source", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := queue.SaveSource(ctx, id, job.LeaseToken, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := queue.AdmitSourceStage(ctx, id, job.LeaseToken, "reading", 210*time.Second); err != nil {
		t.Fatal(err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.CacheIdentity == nil {
		t.Fatalf("current source detail=%+v err=%v", detail, err)
	}
	// An independent Go process sends and settles the paid request, then exits
	// immediately before Complete. The parent keeps the provider fixture alive.
	//nolint:gosec // Re-executes this test binary with a fixed test name and synthetic fixture values.
	crash := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerProviderReadingCrashHelper$")
	crash.Env = append(os.Environ(), "CAIRN_READING_CRASH_HELPER=1", "CAIRN_PROVIDER_URL="+provider.URL,
		"CAIRN_RECOVERY_LINK_ID="+strconv.FormatInt(id, 10), "CAIRN_RECOVERY_JOB_URL="+job.URL,
		"CAIRN_RECOVERY_LEASE="+job.LeaseToken,
		"CAIRN_RECOVERY_REVISION="+strconv.FormatInt(detail.CacheIdentity.ContentRevision, 10),
		"CAIRN_RECOVERY_ATTEMPT="+strconv.Itoa(job.Attempt))
	if output, err := crash.CombinedOutput(); err != nil || posts.Load() != 1 {
		t.Fatalf("paid child exit=%v posts=%d output=%s", err, posts.Load(), string(output))
	}
	var operationKey string
	for _, attempt := range readProviderAttempts(ctx, t, base, "internal") {
		if attempt.LinkID == id && attempt.Stage == "reading" {
			operationKey = attempt.OperationKey
			if attempt.State != "responded" || attempt.ResponseID != responseID {
				t.Fatalf("reading attempt=%+v", attempt)
			}
		}
	}
	if operationKey == "" {
		t.Fatal("settled reading permit is missing")
	}
	operator := cairn.NewClient(base, "operator", httpClient)
	readingResult := enrich.ReadingResult{AITitle: "用于验证阅读恢复的中文标题", OriginalLanguage: "en",
		TranslatedText: "夹具译文", Summary: "夹具摘要", Model: "grok-test"}
	if _, err := operator.RecoverProviderReading(ctx, operationKey, responseID, "ops@example.org",
		readingResult); err == nil {
		t.Fatal("active lease unexpectedly recovered")
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths come from this isolated local-integration harness; no shell is used.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-providerrecovery", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	newQueue := cairn.NewClient(base, "internal", httpClient)
	newOperator := cairn.NewClient(base, "operator", httpClient)
	bound, err := newOperator.InspectProviderAttempt(ctx, operationKey)
	if err != nil || bound.ResponseID == nil || *bound.ResponseID != responseID {
		t.Fatalf("restarted operator permit=%+v err=%v", bound, err)
	}
	savedSource, err := newQueue.GetSource(ctx, id)
	if err != nil || savedSource == nil {
		t.Fatalf("saved source=%+v err=%v", savedSource, err)
	}
	summary, reading, err := enrich.RetrieveStoredReading(ctx, provider.URL, "fixture-key", responseID,
		*savedSource, httpClient)
	if err != nil || summary.ID != responseID || reading.Model != "grok-test" {
		t.Fatalf("saved response=%+v reading=%+v err=%v", summary, reading, err)
	}
	receipt, err := newOperator.RecoverProviderReading(ctx, operationKey, responseID, "ops@example.org", reading)
	if err != nil || !receipt.Recovered || receipt.ID != id {
		t.Fatalf("reading recovery=%+v err=%v", receipt, err)
	}
	replayed, err := newOperator.RecoverProviderReading(ctx, operationKey, responseID, "ops@example.org", reading)
	if err != nil || replayed != receipt {
		t.Fatalf("replayed reading recovery=%+v err=%v", replayed, err)
	}
	after, err := newQueue.GetBookmark(ctx, id)
	if err != nil || after.Status != "completed" || after.OriginalText != source.OriginalText ||
		after.TranslatedText != "夹具译文" || after.PaidCallUnresolved || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatalf("after recovery=%+v posts=%d gets=%d err=%v", after, posts.Load(), gets.Load(), err)
	}
}

func TestLocalWorkerProviderReadingCrashHelper(t *testing.T) {
	if os.Getenv("CAIRN_READING_CRASH_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	linkID, err := strconv.ParseInt(os.Getenv("CAIRN_RECOVERY_LINK_ID"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := strconv.ParseInt(os.Getenv("CAIRN_RECOVERY_REVISION"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := strconv.Atoi(os.Getenv("CAIRN_RECOVERY_ATTEMPT"))
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	queue := cairn.NewClient(workerURL(t), "internal", httpClient)
	catalog, _, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := enrich.NewResponsesClient(os.Getenv("CAIRN_PROVIDER_URL"), "fixture-key", "grok-test",
		1024, "", httpClient, catalog)
	model.SetPaidAttemptLedger(queue)
	result, err := model.Transform(ctx, enrich.Input{ID: linkID,
		URL: os.Getenv("CAIRN_RECOVERY_JOB_URL"), Attempt: attempt,
		LeaseToken: os.Getenv("CAIRN_RECOVERY_LEASE"), ContentRevision: revision,
		MinRemainingMS: 210_000, SourceText: "Persisted primary source", RelatedLinks: []string{}})
	if err != nil || result.TranslatedText != "夹具译文" {
		t.Fatalf("paid reading=%+v err=%v", result, err)
	}
	os.Exit(0)
}
