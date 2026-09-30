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
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Each helper is a fresh OS process and exercises the actual dashboard HTTP
// handler, Go clients, Worker and D1. Only the paid boundary is a local fixture.
func TestRerankCacheProcessHelper(t *testing.T) {
	if os.Getenv("CAIRN_CACHE_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	judge, err := classify.NewClient(os.Getenv("CAIRN_CACHE_PROVIDER"), "fixture", "jev-1.13.0", http.DefaultClient, catalog)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/rerank-cache/claim") {
			bytes, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				return nil, readErr
			}
			_ = r.Body.Close()
			var claim extension.RerankClaim
			if err := json.Unmarshal(bytes, &claim); err != nil {
				return nil, err
			}
			fmt.Printf("WIRE_HASH=%x\n", sha256.Sum256([]byte(claim.RequestJSON)))
			r.Body = io.NopCloser(strings.NewReader(string(bytes)))
		}
		response, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/complete") && os.Getenv("CAIRN_CACHE_LOSE_COMPLETE") == "1" {
			var receipt extension.RerankReceipt
			err := json.NewDecoder(response.Body).Decode(&receipt)
			_ = response.Body.Close()
			if err != nil || receipt.Status != "completed" {
				return nil, fmt.Errorf("fixture did not commit completion")
			}
			fmt.Printf("COMPLETE_COMMITTED_RESPONSE_LOST key=%s\n", receipt.Key)
			return nil, io.ErrUnexpectedEOF
		}
		return response, nil
	})
	queue := cairn.NewClient(os.Getenv("CAIRN_WORKER_URL"), envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Transport: transport, Timeout: 5 * time.Second})
	budget := extension.DefaultBudget()
	budget.MaxCallsTotal = 3
	if os.Getenv("CAIRN_CACHE_EXHAUSTED") == "1" {
		budget.MaxCallsTotal = 1
	}
	service := extension.NewService(extension.Flags{Rerank: true}, budget, judge)
	service.SetBudgetStore(queue)
	service.SetRerankStore(queue)
	app := dashboard.New(ctx, health.NewTracker(), queue, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	app.SetExtensions(service)
	defer app.Drain(time.Second)
	filters := "topics=eng"
	if os.Getenv("CAIRN_CACHE_FILTERS") == "topics=eng&source=x" {
		filters = "topics=eng&source=x"
	}
	body, _ := json.Marshal(map[string]any{"query": "synthetic", "limit": 1, "filters": filters})
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/api/rerank", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	defer func() { _ = response.Body.Close() }()
	var result extension.RerankResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != 200 {
		t.Fatalf("HTTP status=%d error=%v", response.StatusCode, err)
	}
	data, _ := json.Marshal(result)
	fmt.Printf("CACHE_RESULT=%s\n", data)
}

func TestLocalWorkerRerankCacheAcrossProcesses(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	appCall := func(method, path, body string) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode >= 300 {
			t.Fatalf("fixture HTTP status=%d err=%v", response.StatusCode, err)
		}
		return data
	}
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), http.DefaultClient)
	ids := []int64{}
	for n := 0; n < 3; n++ {
		data := appCall("POST", "/api/links", fmt.Sprintf(`{"url":"https://x.com/cache/status/%d","note":"synthetic note"}`, 98001+n))
		var created struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(data, &created); err != nil || created.ID < 1 {
			t.Fatal("create failed")
		}
		ids = append(ids, created.ID)
		if n < 2 {
			if _, err := queue.ApplyV2Override(ctx, created.ID, cairn.V2Override{Field: "topics", Term: "eng", Action: "accept", OperationKey: fmt.Sprintf("cache-topic-%d", n)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var wireMu sync.Mutex
	wireHashes := []string{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		var payload struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.Model != "jev-1.13.0" || len(payload.Questions) != 1 {
			w.WriteHeader(400)
			return
		}
		wireMu.Lock()
		wireHashes = append(wireHashes, fmt.Sprintf("%x", sha256.Sum256(data)))
		wireMu.Unlock()
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if n == 3 {
			appCall("DELETE", fmt.Sprintf("/api/links/%d", ids[0]), "")
		}
		answers := map[string]any{}
		for id := range payload.Questions {
			answers[id] = map[string]any{"type": "score", "score": 2, "legend": map[string]string{"0": "不相关", "1": "略有关系", "2": "明显相关", "3": "高度相关"}, "probabilities": map[string]float64{"0": 0, "1": 0, "2": 1, "3": 0}, "confidence": 1}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers})
	}))
	defer func() { unblock(); provider.Close() }()
	type child struct {
		output string
		err    error
	}
	run := func(filters string, lose, exhausted bool) child {
		childCtx, stop := context.WithTimeout(ctx, 25*time.Second)
		defer stop()
		command := exec.CommandContext(childCtx, "go", "test", "./tests/localintegration", "-run", "^TestRerankCacheProcessHelper$", "-count=1", "-v")
		command.Dir = "../.."
		boolEnv := func(b bool) string {
			if b {
				return "1"
			}
			return "0"
		}
		command.Env = append(os.Environ(), "CAIRN_CACHE_CHILD=1", "CAIRN_CACHE_PROVIDER="+provider.URL, "CAIRN_CACHE_FILTERS="+filters, "CAIRN_CACHE_LOSE_COMPLETE="+boolEnv(lose), "CAIRN_CACHE_EXHAUSTED="+boolEnv(exhausted))
		output, err := command.CombinedOutput()
		return child{string(output), err}
	}
	check := func(c child, applied bool, status string, wantID int64) extension.RerankResult {
		t.Helper()
		if c.err != nil {
			t.Fatalf("child %v: %s", c.err, c.output)
		}
		var result extension.RerankResult
		found := false
		for _, line := range strings.Split(c.output, "\n") {
			if strings.HasPrefix(line, "CACHE_RESULT=") {
				found = true
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "CACHE_RESULT=")), &result); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !found || result.Applied != applied || result.CacheStatus != status || result.Scope != "current_candidates" {
			t.Fatalf("unexpected child: %s", c.output)
		}
		if wantID > 0 && (len(result.Candidates) != 1 || result.Candidates[0].ID != fmt.Sprint(wantID)) {
			t.Fatalf("filter widened: %+v", result)
		}
		if wantID == 0 && len(result.Candidates) != 0 {
			t.Fatalf("deleted candidate survived: %+v", result)
		}
		return result
	}
	firstDone := make(chan child, 1)
	go func() { firstDone <- run("topics=eng", true, false) }()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		unblock()
		t.Fatalf("model did not start: %+v", <-firstDone)
	}
	check(run("topics=eng", false, false), false, "pending", ids[1])
	unblock()
	first := <-firstDone
	ranked := check(first, true, "miss", ids[1])
	if ranked.NextBeforeID == nil || *ranked.NextBeforeID != ids[1] || !strings.Contains(first.output, "COMPLETE_COMMITTED_RESPONSE_LOST") {
		t.Fatalf("cursor/recovery not exercised: %s", first.output)
	}
	wireMu.Lock()
	hash := wireHashes[0]
	wireMu.Unlock()
	if !strings.Contains(first.output, "WIRE_HASH="+hash) {
		t.Fatal("cached request differs from actual provider body")
	}
	check(run("topics=eng", false, true), true, "hit", ids[1]) // Even a tightened, exhausted budget permits a paid-free hit.
	if calls.Load() != 1 {
		t.Fatal("restart repeated inference")
	}
	check(run("topics=eng&source=x", false, false), true, "miss", ids[1]) // Scope is part of the key.
	if calls.Load() != 2 {
		t.Fatal("scope change failed to create distinct inference")
	}
	note := "synthetic revised note"
	if _, err := queue.UpdateCuration(ctx, ids[1], cairn.CurationUpdate{Why: &note}); err != nil {
		t.Fatal(err)
	}
	check(run("topics=eng", false, false), false, "failed", ids[1]) // Version change cannot return old success; per-item allowance exhausted.
	check(run("topics=eng", false, false), false, "failed", ids[1]) // Failed ownership persists through process restart.
	var completedKey string
	for _, line := range strings.Split(first.output, "\n") {
		if strings.HasPrefix(line, "COMPLETE_COMMITTED_RESPONSE_LOST key=") {
			completedKey = strings.TrimPrefix(line, "COMPLETE_COMMITTED_RESPONSE_LOST key=")
		}
	}
	appCall("DELETE", fmt.Sprintf("/api/links/%d", ids[1]), "")
	if _, err := queue.GetRerank(ctx, completedKey); err == nil {
		t.Fatal("deleted result remains readable")
	}
	check(run("topics=eng", false, false), false, "invalidated", 0) // Third actual model response races with deletion; final filtered reread is empty.
	if calls.Load() != 3 {
		t.Fatalf("actual fixture provider calls=%d", calls.Load())
	}
	t.Log("PASS: seven fresh Go processes, concurrent ownership, lost completion response recovery, request byte identity, exhausted-budget hit, filter/cursor scope, human revision invalidation, failed-state restart, deletion during inference; three local provider calls, zero paid calls")
}
