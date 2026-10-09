package localintegration

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// A full provider result reaches Go, but the Worker settlement write fails.
// Exiting that Go process must leave the paid attempt unresolved, not reusable.
func TestLocalWorkerProviderSettlementFailureSurvivesProcessExit(t *testing.T) {
	base := workerURL(t)
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("local D1 fixture configuration is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/responses" {
			t.Errorf("unexpected provider request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Errorf("read provider POST: %v", err)
			return
		}
		posts.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "resp_settlement_fixture", "status": "completed", "model": "grok-test",
			"output": []any{
				map[string]any{"type": "x_search_call", "status": "completed"},
				map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text",
					"text": `{"original_text":"Fixture source","original_language":"en","context_text":"","related_links":[],"image_urls":[]}`}}},
			},
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
		})
	}))
	defer provider.Close()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	id := createLink(t, base, "app")
	//nolint:gosec // Re-executes this test binary against isolated local HTTP fixtures.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerProviderSettlementFailureHelper$")
	child.Env = append(os.Environ(), "CAIRN_SETTLEMENT_FAILURE_HELPER=1", "CAIRN_PROVIDER_URL="+provider.URL)
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "settlement failed after full provider response") {
		t.Fatalf("provider settlement child exit=%v output=%s", err, string(output))
	}
	if posts.Load() != 1 {
		t.Fatalf("provider POST count=%d, want 1", posts.Load())
	}
	items := readProviderAttempts(ctx, t, base, "internal")
	var operationKey string
	for _, item := range items {
		if item.LinkID == id {
			if item.Stage != "reading" || item.State != "reserved" || item.ResponseID != "" {
				t.Fatalf("unsettled provider attempt=%+v", item)
			}
			operationKey = item.OperationKey
		}
	}
	if operationKey == "" {
		t.Fatal("provider attempt was not reserved before the response")
	}
	operator := cairn.NewClient(base, "operator", &http.Client{Timeout: 10 * time.Second})
	unknown, err := operator.InspectProviderAttempt(ctx, operationKey)
	if err != nil || unknown.State != "reserved" || unknown.ResponseID != nil ||
		unknown.CurrentPaidUnresolved == nil || *unknown.CurrentPaidUnresolved != 1 {
		t.Fatalf("unsettled paid attempt inspection=%+v error=%v", unknown, err)
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths come from this isolated local-integration harness.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-providersettle", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	claim, err := restarted.Claim(ctx)
	var gateErr *cairn.APIError
	// An unresolved paid receipt keeps the shared source circuit paused. Both
	// an empty queue and this explicit gate must preserve the original attempt.
	paused := errors.As(err, &gateErr) && gateErr.StatusCode == http.StatusServiceUnavailable && gateErr.Code == "component_paused"
	if err != nil && !paused || claim != nil || posts.Load() != 1 {
		t.Fatalf("unsettled attempt was reclaimed or resent: claim=%+v posts=%d error=%v", claim, posts.Load(), err)
	}
}

func TestLocalWorkerProviderSettlementFailureHelper(t *testing.T) {
	if os.Getenv("CAIRN_SETTLEMENT_FAILURE_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var intercepted atomic.Int32
	workerHTTP := &http.Client{Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/api/enrichment/provider-attempts/settle" {
				intercepted.Add(1)
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{"error":"fixture_unavailable"}`)), Request: request}, nil
			}
			return http.DefaultTransport.RoundTrip(request)
		}),
	}
	queue := cairn.NewClient(workerURL(t), "internal", workerHTTP)
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	catalog, _, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := enrich.NewResponsesClient(os.Getenv("CAIRN_PROVIDER_URL"), "fixture-key", "grok-test",
		1024, "", &http.Client{Timeout: 10 * time.Second}, catalog)
	model.SetPaidAttemptLedger(queue)
	worker := processor.NewStaged(queue, model, nil, "", "", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(20 * time.Second)
	job, err := queue.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim paid source job=%+v error=%v", job, err)
	}
	if err := worker.Process(ctx, job); err == nil || intercepted.Load() != 1 {
		t.Fatalf("settlement failure was not reported: error=%v intercepted=%d", err, intercepted.Load())
	}
	_, _ = os.Stdout.WriteString("settlement failed after full provider response\n")
	os.Exit(0)
}
