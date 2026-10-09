package localintegration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// This uses the real Go Responses adapter and real local Worker/D1, with only
// the paid provider replaced by an in-process HTTP fixture.
func TestLocalWorkerProviderAttemptLedger(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), httpClient)
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	catalog, _, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	var loseResponse atomic.Bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		posts.Add(1)
		if loseResponse.Load() {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack provider: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		var payload struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider: %v", err)
			return
		}
		var output []any
		var content string
		if len(payload.Tools) > 0 {
			output = append(output, map[string]any{"type": "x_search_call", "status": "completed"})
			content = `{"original_text":"Fixture original text","original_language":"en","context_text":"","related_links":[],"image_urls":[]}`
		} else {
			content = `{"ai_title":"用于验证付费调用账本的标题","original_language":"en","translated_text":"夹具中文译文","summary":"夹具摘要"}`
		}
		output = append(output, map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": content}}})
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"id": "resp_local_fixture", "status": "completed",
			"model": "grok-test", "output": output, "usage": map[string]any{
				"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "cost_in_usd_ticks": 1234,
				"server_side_tool_usage_details": map[string]any{"x_search_calls": len(payload.Tools)}}})
	}))
	defer modelServer.Close()
	model := enrich.NewResponsesClient(modelServer.URL, "fixture-key", "grok-test", 1024, "", httpClient, catalog)
	model.SetPaidAttemptLedger(queue)
	worker := processor.NewStaged(queue, model, nil, "", "", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(10 * time.Second)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id || job.ContentRevision < 1 {
		t.Fatalf("claim=%+v error=%v", job, err)
	}
	if err := worker.Process(ctx, job); err != nil {
		t.Fatalf("process: %v", err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Status != "completed" || detail.OriginalText != "Fixture original text" || posts.Load() != 1 {
		t.Fatalf("detail=%+v posts=%d error=%v", detail, posts.Load(), err)
	}
	items := readProviderAttempts(ctx, t, base, "internal")
	var source, reading int
	var sourceOperation string
	for _, item := range items {
		if item.LinkID != id {
			continue
		}
		if item.State != "responded" || item.ResponseID != "resp_local_fixture" || item.CostUSDTicks != 1234 {
			t.Fatalf("attempt=%+v", item)
		}
		if item.Stage == "fetch" {
			source++
			sourceOperation = item.OperationKey
		}
		if item.Stage == "reading" {
			reading++
			sourceOperation = item.OperationKey
		}
	}
	if source != 0 || reading != 1 {
		t.Fatalf("source=%d reading=%d items=%+v", source, reading, items)
	}
	operator := cairn.NewClient(base, "operator", httpClient)
	known, err := operator.InspectProviderAttempt(ctx, sourceOperation)
	if err != nil || known.State != "responded" || known.ResponseID == nil ||
		*known.ResponseID != "resp_local_fixture" || known.LinkID == nil || *known.LinkID != id {
		t.Fatalf("known provider inspection = %+v, err=%v", known, err)
	}
	if _, err := queue.InspectProviderAttempt(ctx, sourceOperation); err == nil {
		t.Fatal("enricher token inspected an operator-only permit")
	}
	// A process loses the provider response after the server received its POST.
	loseResponse.Store(true)
	failedID := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	failedJob, err := queue.Claim(ctx)
	if err != nil || failedJob == nil || failedJob.ID != failedID {
		t.Fatalf("failed claim=%+v error=%v", failedJob, err)
	}
	if err := worker.Process(ctx, failedJob); err == nil {
		t.Fatal("lost response unexpectedly succeeded")
	}
	if posts.Load() != 2 {
		t.Fatalf("provider posts=%d, want three", posts.Load())
	}
	items = readProviderAttempts(ctx, t, base, "internal")
	var unresolved int
	var unknownOperation string
	for _, item := range items {
		if item.LinkID == failedID && item.State == "reserved" {
			unresolved++
			unknownOperation = item.OperationKey
		}
	}
	if unresolved != 1 {
		t.Fatalf("unresolved=%d items=%+v", unresolved, items)
	}
	unknown, err := operator.InspectProviderAttempt(ctx, unknownOperation)
	if err != nil || unknown.State != "reserved" || unknown.ResponseID != nil ||
		unknown.CurrentPaidUnresolved == nil || *unknown.CurrentPaidUnresolved != 1 {
		t.Fatalf("unknown provider inspection = %+v, err=%v", unknown, err)
	}
	if claimed, err := queue.Claim(ctx); err != nil || claimed != nil {
		t.Fatalf("unknown result reclaimed: %+v %v", claimed, err)
	}
}

type providerAttemptView struct {
	OperationKey string `json:"operation_key"`
	LinkID       int64  `json:"link_id"`
	Stage        string `json:"stage"`
	State        string `json:"state"`
	ResponseID   string `json:"response_id"`
	CostUSDTicks int64  `json:"cost_usd_ticks"`
}

func readProviderAttempts(ctx context.Context, t *testing.T, base, token string) []providerAttemptView {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/enrichment/provider-attempts?state=all", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list attempts HTTP %d", response.StatusCode)
	}
	var body struct {
		Items []providerAttemptView `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Items
}
