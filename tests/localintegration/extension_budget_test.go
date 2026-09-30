package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// This helper is run in a new OS process for every attempt. Only the paid
// provider is a local HTTP fixture; reservation uses the real Worker/D1.
func TestExtensionBudgetProcessHelper(t *testing.T) {
	if os.Getenv("CAIRN_BUDGET_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	judge, err := classify.NewClient(os.Getenv("CAIRN_BUDGET_PROVIDER"), "fixture", "jev-1.13.0", http.DefaultClient, catalog)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("CAIRN_BUDGET_LOSE_GRANT") == "1" {
		client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			response, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			if strings.HasSuffix(r.URL.Path, "/extension-budget/reserve") {
				var grant extension.Grant
				decodeErr := json.NewDecoder(response.Body).Decode(&grant)
				_ = response.Body.Close()
				if decodeErr != nil || !grant.Granted {
					return nil, fmt.Errorf("fixture did not commit a grant")
				}
				fmt.Println("GRANT_COMMITTED_RESPONSE_LOST")
				return nil, io.ErrUnexpectedEOF
			}
			return response, nil
		})
	}
	queue := cairn.NewClient(os.Getenv("CAIRN_WORKER_URL"), envOr("CAIRN_ENRICHER_TOKEN", "internal"), client)
	budget := extension.DefaultBudget()
	budget.MaxCallsTotal = 3
	service := extension.NewService(extension.Flags{Rerank: true}, budget, judge)
	service.SetBudgetStore(queue)
	result := service.RerankCandidates(ctx, "synthetic query", []extension.Candidate{{ID: os.Getenv("CAIRN_BUDGET_ITEM"), Text: "synthetic material", Allowed: true}})
	fmt.Printf("BUDGET_APPLIED=%t\n", result.Applied)
}

func TestLocalWorkerExtensionBudgetAcrossProcesses(t *testing.T) {
	base := workerURL(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "jev-1.13.0" {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		answers := map[string]any{}
		for id := range payload.Questions {
			answers[id] = map[string]any{"type": "score", "score": 2, "legend": map[string]string{"0": "不相关", "1": "略有关系", "2": "明显相关", "3": "高度相关"}, "probabilities": map[string]float64{"0": 0, "1": 0, "2": 1, "3": 0}, "confidence": 1}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 10}})
	}))
	defer provider.Close()
	ids := []int64{}
	for n := 0; n < 3; n++ {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/links", strings.NewReader(fmt.Sprintf(`{"url":"https://x.com/fixture/status/%d"}`, 99001+n)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var created struct {
			ID int64 `json:"id"`
		}
		err = json.NewDecoder(response.Body).Decode(&created)
		_ = response.Body.Close()
		if err != nil || created.ID < 1 {
			t.Fatalf("create status=%d error=%v", response.StatusCode, err)
		}
		ids = append(ids, created.ID)
	}
	run := func(id int64, lose, want bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "go", "test", "./tests/localintegration", "-run", "^TestExtensionBudgetProcessHelper$", "-count=1", "-v")
		command.Dir = "../.."
		command.Env = append(os.Environ(), "CAIRN_BUDGET_CHILD=1", "CAIRN_BUDGET_PROVIDER="+provider.URL, "CAIRN_BUDGET_ITEM="+strconv.FormatInt(id, 10), "CAIRN_BUDGET_LOSE_GRANT="+map[bool]string{true: "1", false: "0"}[lose])
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("child: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), fmt.Sprintf("BUDGET_APPLIED=%t", want)) {
			t.Fatalf("unexpected child result: %s", output)
		}
		if lose && !strings.Contains(string(output), "GRANT_COMMITTED_RESPONSE_LOST") {
			t.Fatal("response-loss fixture did not commit")
		}
	}
	run(ids[0], false, true)
	run(ids[0], false, true)  // Fresh process, same persisted item allowance.
	run(ids[0], false, false) // Per-item limit survives both exits.
	run(ids[1], true, false)  // Global reservation committed, response lost: no model.
	run(ids[2], false, false) // New item/process cannot reset global allowance.
	if calls.Load() != 2 {
		t.Fatalf("actual provider HTTP calls=%d want 2", calls.Load())
	}
	t.Log("PASS: five separate Go processes; two actual local provider calls; per-item/global D1 limits and unknown grant outcome survive restart; zero paid calls")
}
