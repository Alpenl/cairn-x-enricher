package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func dependencySpec(t *testing.T, dependencies map[string][]string) QuestionSpec {
	t.Helper()
	base, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	for index := range base.Questions {
		base.Questions[index].DependsOn = dependencies[base.Questions[index].ID]
	}
	return base
}

func TestPlanBatchesOrdersDependenciesDeterministically(t *testing.T) {
	spec := dependencySpec(t, map[string][]string{
		"topic_eval": {"topic_llm"},
		"use":        {"form"},
	})
	batches, err := PlanBatches(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}
	first := batchIDs(batches[0])
	if containsString(first, "topic_eval") || containsString(first, "use") {
		t.Fatalf("a dependent question leaked into the first batch: %v", first)
	}
	second := batchIDs(batches[1])
	if !containsString(second, "topic_eval") || !containsString(second, "use") {
		t.Fatalf("dependent questions missing from the second batch: %v", second)
	}
	if first[0] != "form" || !sortedStrings(first) || !sortedStrings(second) {
		t.Fatalf("batch order is not deterministic: %v / %v", first, second)
	}
	// The production spec has no dependencies and stays a single request.
	production, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := PlanBatches(production)
	if err != nil || len(plain) != 1 {
		t.Fatalf("a dependency-free spec must be one batch: %d %v", len(plain), err)
	}
}

func TestPlanBatchesRejectsCyclesAndUnknownDependencies(t *testing.T) {
	cycle := dependencySpec(t, map[string][]string{
		"topic_llm":  {"topic_eval"},
		"topic_eval": {"topic_llm"},
	})
	if _, err := PlanBatches(cycle); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle must be rejected, got %v", err)
	}
	unknown := dependencySpec(t, map[string][]string{"topic_llm": {"not_a_question"}})
	if _, err := PlanBatches(unknown); err == nil || !strings.Contains(err.Error(), "unknown question") {
		t.Fatalf("an unknown dependency must be rejected, got %v", err)
	}
}

func TestPlanChunksIsDeterministicAndBounded(t *testing.T) {
	spec, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := PlanChunks(spec, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	seen := map[string]bool{}
	for index, chunk := range chunks {
		if len(chunk.Questions) > 2 {
			t.Fatalf("chunk %d exceeds the bound: %d", index, len(chunk.Questions))
		}
		if chunk.Index != index || chunk.Total != 3 {
			t.Fatalf("chunk metadata = %+v", chunk)
		}
		for _, question := range chunk.Questions {
			if seen[question.ID] {
				t.Fatalf("question %s appears in two chunks", question.ID)
			}
			seen[question.ID] = true
		}
	}
	if len(seen) != len(spec.Questions) {
		t.Fatalf("chunks lost questions: %d/%d", len(seen), len(spec.Questions))
	}
	// A deterministic retry sends exactly the same chunks.
	again, err := PlanChunks(spec, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(chunks) {
		t.Fatalf("chunk plan is not deterministic")
	}
	for index := range chunks {
		if batchIDs(Batch{Questions: chunks[index].Questions})[0] != batchIDs(Batch{Questions: again[index].Questions})[0] {
			t.Fatal("chunk contents changed between identical plans")
		}
	}
}

// TestEvaluateBatchedIsolatesAFailedChunk is the B04-T06 regression: one failed
// request leaves the other answers intact, marks the missing questions and
// reports partial coverage instead of pretending the run is complete.
func TestEvaluateBatchedIsolatesAFailedChunk(t *testing.T) {
	catalog := testCatalog()
	spec, err := CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	// The first chunk (sorted by ID) is [form, topic_eng]; failing it must not
	// affect the other two chunks.
	failing := map[string]bool{"form": true, "topic_eng": true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		for id := range body.Questions {
			if failing[id] {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		answers := map[string]any{}
		for id, question := range body.Questions {
			if question.Type == TypeNoul {
				answers[id] = map[string]any{"type": "noul", "noul": 0.9}
				continue
			}
			var options map[string]string
			_ = json.Unmarshal(question.Criteria, &options)
			pick := "method"
			for option := range options {
				pick = option
				break
			}
			probabilities := map[string]float64{}
			for option := range options {
				if option == pick {
					probabilities[option] = 1
				} else {
					probabilities[option] = 0
				}
			}
			answers[id] = map[string]any{"type": "choice", "choice": pick, "probabilities": probabilities}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": answers})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	client.spec = spec
	merged, err := client.EvaluateBatched(context.Background(), Input{OriginalText: "text"}, 2)
	if err != nil {
		t.Fatalf("a partial failure must not abort the run: %v", err)
	}
	if merged.Coverage != "partial" {
		t.Fatalf("coverage = %q, want partial", merged.Coverage)
	}
	if len(merged.Missing) != 2 || !containsString(merged.Missing, "form") || !containsString(merged.Missing, "topic_eng") {
		t.Fatalf("missing = %v", merged.Missing)
	}
	if len(merged.Judgments) != len(spec.Questions)-2 {
		t.Fatalf("judgments = %d, want %d", len(merged.Judgments), len(spec.Questions)-2)
	}
}

// TestEvaluateBatchedAbortsOnAComponentFault proves a 401 is not contained: it
// is a component condition, not a per-chunk data problem.
func TestEvaluateBatchedAbortsOnAComponentFault(t *testing.T) {
	catalog := testCatalog()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.EvaluateBatched(context.Background(), Input{OriginalText: "text"}, 2)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a component fault must abort: %v", err)
	}
}

func batchIDs(batch Batch) []string {
	ids := make([]string, 0, len(batch.Questions))
	for _, question := range batch.Questions {
		ids = append(ids, question.ID)
	}
	return ids
}

func sortedStrings(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] > values[index] {
			return false
		}
	}
	return true
}

// TestPartialCoverageDecidesHonestly proves a partial run still produces a
// decision over the answered fields while recording what is missing (R2-13).
func TestPartialCoverageDecidesHonestly(t *testing.T) {
	spec, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	raw := RawJudgments{
		SpecID: spec.SpecID, Coverage: "partial", Missing: []string{"topic_llm", "use"},
		Judgments: map[string]RawJudgment{
			"topic_eval": {QuestionID: "topic_eval", Kind: QuestionNoul, Dimension: "topics", TermID: "eval", Noul: ptr(0.9)},
			"form":       {QuestionID: "form", Kind: QuestionChoice, Dimension: "form", Choice: "method", Probabilities: map[string]float64{"method": 1}},
		},
	}
	proposals, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatalf("a partial run must still decide: %v", err)
	}
	if len(proposals.Topics) != 1 || proposals.Topics[0] != "eval" {
		t.Fatalf("partial topics = %v", proposals.Topics)
	}
	found := false
	for _, incomplete := range proposals.Incomplete {
		if incomplete == "missing:topic_llm" || incomplete == "missing:use" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing questions must be recorded: %v", proposals.Incomplete)
	}
}
