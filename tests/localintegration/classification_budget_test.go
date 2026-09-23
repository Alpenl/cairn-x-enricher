package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestClassificationBudgetProcessHelper(t *testing.T) {
	if os.Getenv("CAIRN_CLASSIFY_BUDGET_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/classification-budget/reserve") && os.Getenv("CAIRN_CLASSIFY_LOSE_GRANT") == "1" {
			var grant classify.CallGrant
			err := json.NewDecoder(response.Body).Decode(&grant)
			_ = response.Body.Close()
			if err != nil || !grant.Granted {
				return nil, fmt.Errorf("fixture did not commit classification grant")
			}
			fmt.Println("CLASSIFICATION_GRANT_COMMITTED_RESPONSE_LOST")
			return nil, io.ErrUnexpectedEOF
		}
		return response, nil
	})
	queue := cairn.NewClient(os.Getenv("CAIRN_WORKER_URL"), envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Transport: transport, Timeout: 5 * time.Second})
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, err := classify.NewClient(os.Getenv("CAIRN_CLASSIFY_BUDGET_PROVIDER"), "fixture", "jev-1.13.0", http.DefaultClient, catalog)
	if err != nil {
		t.Fatal(err)
	}
	limits := classify.DefaultCallBudgetLimits()
	limits.MaxCallsTotal = 3
	limits.MaxCallsPerItem = 2
	if err := queue.SetClassificationBudgetLimits(limits); err != nil {
		t.Fatal(err)
	}
	if err := client.SetCallBudget(queue, limits); err != nil {
		t.Fatal(err)
	}
	p := processor.NewStaged(queue, nil, client, catalog.Version, "jev-1.13.0", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	done, failed, err := p.RunClassifications(ctx, 1)
	fmt.Printf("CLASSIFICATION_RESULT done=%d failed=%d paused=%t\n", done, failed, err != nil && strings.Contains(err.Error(), "paused"))
	if err != nil && !strings.Contains(err.Error(), "paused") {
		t.Fatal(err)
	}
}

func TestLocalWorkerClassificationBudgetAcrossProcesses(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, token, http.DefaultClient)
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model     string `json:"model"`
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "jev-1.13.0" {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		answers := map[string]any{}
		for id, q := range payload.Questions {
			if q.Type == "noul" {
				answers[id] = map[string]any{"type": "noul", "noul": .9}
				continue
			}
			var options map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &options) != nil {
				w.WriteHeader(400)
				return
			}
			probabilities := map[string]float64{}
			pick := ""
			for option := range options {
				probabilities[option] = 0
				if pick == "" {
					pick = option
				}
			}
			probabilities[pick] = 1
			answers[id] = map[string]any{"type": "choice", "choice": pick, "probabilities": probabilities, "confidence": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}})
	}))
	defer provider.Close()
	client, err := classify.NewClient(provider.URL, "fixture", "jev-1.13.0", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, client.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, client, "jev-1.13.0")
	seed := func(n int) int64 {
		t.Helper()
		created := postJSON(ctx, t, base+"/api/links", envOr("CAIRN_APP_TOKEN", "app"), map[string]any{"url": fmt.Sprintf("https://x.com/classbudget/status/%d", 97000+n)})
		id := int64(created["id"].(float64))
		job, err := queue.Claim(ctx)
		if err != nil || job == nil || job.ID != id {
			t.Fatalf("source claim: %+v %v", job, err)
		}
		lease := job.LeaseToken
		source := enrich.Source{OriginalText: "Synthetic source about evaluating language models.", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "source-fixture"}
		if err := queue.SaveSource(ctx, id, lease, source); err != nil {
			t.Fatal(err)
		}
		if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
			t.Fatal(err)
		}
		return id
	}
	run := func(lose bool, wantDone int, wantPaused bool) {
		t.Helper()
		childCtx, stop := context.WithTimeout(ctx, 25*time.Second)
		defer stop()
		command := exec.CommandContext(childCtx, "go", "test", "./tests/localintegration", "-run", "^TestClassificationBudgetProcessHelper$", "-count=1", "-v")
		command.Dir = "../.."
		loss := "0"
		if lose {
			loss = "1"
		}
		command.Env = append(os.Environ(), "CAIRN_CLASSIFY_BUDGET_CHILD=1", "CAIRN_CLASSIFY_BUDGET_PROVIDER="+provider.URL, "CAIRN_CLASSIFY_LOSE_GRANT="+loss)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("child %v: %s", err, output)
		}
		if !strings.Contains(string(output), fmt.Sprintf("CLASSIFICATION_RESULT done=%d failed=0 paused=%t", wantDone, wantPaused)) {
			t.Fatalf("unexpected child: %s", output)
		}
		if lose && !strings.Contains(string(output), "CLASSIFICATION_GRANT_COMMITTED_RESPONSE_LOST") {
			t.Fatal("lost response did not commit grant")
		}
	}
	id := seed(1)
	run(false, 1, false)
	if err := queue.RetryClassification(ctx, id); err != nil {
		t.Fatal(err)
	}
	run(false, 1, false)
	if err := queue.RetryClassification(ctx, id); err != nil {
		t.Fatal(err)
	}
	run(false, 0, false)
	runBefore, err := queue.GetRuns(ctx, id)
	if err != nil || len(runBefore) != 2 {
		t.Fatalf("successful stored calls=%d error=%v", len(runBefore), err)
	}
	seed(2)
	run(true, 0, true)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/links/%d", base, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete %d", response.StatusCode)
	}
	third := seed(3)
	run(false, 0, true)
	statusRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/enrichment/classifications/%d", base, third), nil)
	if err != nil {
		t.Fatal(err)
	}
	statusRequest.Header.Set("Authorization", "Bearer "+token)
	statusResponse, err := http.DefaultClient.Do(statusRequest)
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Attempts *int   `json:"attempts"`
		Status   string `json:"status"`
	}
	decodeErr := json.NewDecoder(statusResponse.Body).Decode(&pending)
	_ = statusResponse.Body.Close()
	if decodeErr != nil || statusResponse.StatusCode != 200 || pending.Attempts == nil || *pending.Attempts != 0 || pending.Status != "pending" {
		t.Fatalf("exhaustion burned attempt: status=%d state=%+v error=%v", statusResponse.StatusCode, pending, decodeErr)
	}

	if calls.Load() != 2 {
		t.Fatalf("actual provider calls=%d want2", calls.Load())
	}
	t.Log("PASS: five fresh Go processors; two actual local provider HTTP calls; persisted per-item retry limit; committed grant response lost with zero inference; deletion does not refund global usage; new process/global exhaustion stops before claim; zero paid calls")
}
