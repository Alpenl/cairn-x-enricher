package classify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// reuseServer answers any provider request and counts the calls and the question
// ids each request contained, so a partial re-evaluation can be asserted on the
// wire rather than on its own bookkeeping.
func reuseServer(t *testing.T, calls *int32, questionIDs *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		var body struct {
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		ids := make([]string, 0, len(body.Questions))
		answers := map[string]any{}
		for id, question := range body.Questions {
			ids = append(ids, id)
			switch question.Type {
			case TypeNoul:
				answers[id] = map[string]any{"type": "noul", "noul": 0.9}
			case TypeChoice:
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
		}
		*questionIDs = append(*questionIDs, ids)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": answers, "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}})
	}))
}

func TestEvaluateReusingReusesEverythingWhenNothingChanged(t *testing.T) {
	var calls int32
	var ids [][]string
	server := reuseServer(t, &calls, &ids)
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Evaluate(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("first evaluation calls = %d", calls)
	}
	merged, err := client.EvaluateReusing(context.Background(), Input{OriginalText: "text"}, &first, first.BatchSemantics)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("an unchanged re-evaluation must make zero provider calls, got %d", calls)
	}
	if len(merged.Reused) != len(client.Spec().Questions) || merged.Coverage != "complete" {
		t.Fatalf("reuse result = %+v", merged)
	}
	if len(merged.Judgments) != len(first.Judgments) {
		t.Fatalf("merged judgments = %d, want %d", len(merged.Judgments), len(first.Judgments))
	}
}

func TestEvaluateReusingInfersOnlyTheChangedQuestion(t *testing.T) {
	var calls int32
	var ids [][]string
	server := reuseServer(t, &calls, &ids)
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Evaluate(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	// Change exactly one question's criteria, as a redefinition would.
	next := client.Spec()
	for index := range next.Questions {
		if next.Questions[index].ID == "topic_llm" {
			next.Questions[index].Criteria = mustJSON(map[string]string{"true": "新定义", "false": "否"})
		}
	}
	hash, err := HashSpec(next)
	if err != nil {
		t.Fatal(err)
	}
	next.SemanticHash = hash
	client.spec = next
	before := atomic.LoadInt32(&calls)
	merged, err := client.EvaluateReusing(context.Background(), Input{OriginalText: "text"}, &first, first.BatchSemantics)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != before+1 {
		t.Fatalf("expected exactly one new provider call, got %d", atomic.LoadInt32(&calls)-before)
	}
	lastRequest := ids[len(ids)-1]
	if len(lastRequest) != 1 || lastRequest[0] != "topic_llm" {
		t.Fatalf("the provider request must contain only the changed question, got %v", lastRequest)
	}
	if merged.Coverage != "complete" || len(merged.Judgments) != len(next.Questions) {
		t.Fatalf("merged run is incomplete: %+v", merged)
	}
	// The changed question's answer came from the provider; the rest are reused.
	if !containsString(merged.Reused, "form") || containsString(merged.Reused, "topic_llm") {
		t.Fatalf("reuse list = %v", merged.Reused)
	}
}

func TestEvaluateReusingReinfersWhenEvidenceOrModelChanges(t *testing.T) {
	var calls int32
	var ids [][]string
	server := reuseServer(t, &calls, &ids)
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Evaluate(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	// Different evidence hash: nothing may be reused.
	plan, err := PlanReuse(&first, client.Spec(), "different-evidence", "jev-latest", "batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Reusable) != 0 || len(plan.ToInfer) != len(client.Spec().Questions) {
		t.Fatalf("evidence change must invalidate every question: %+v", plan)
	}
	// Different model: nothing may be reused.
	plan, err = PlanReuse(&first, client.Spec(), first.EvidenceHash, "jev-other", "batch-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Reusable) != 0 {
		t.Fatalf("model change must invalidate every question: %+v", plan)
	}
	// A different batch semantics invalidates reuse even when everything else
	// matches.
	plan, err = PlanReuse(&first, client.Spec(), first.EvidenceHash, "jev-latest", "chunks-of-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Reusable) != 0 {
		t.Fatalf("a batch semantics change must invalidate every question: %+v", plan)
	}
	// A previous run without an evidence hash cannot be reused safely.
	stripped := first
	stripped.EvidenceHash = ""
	plan, err = PlanReuse(&stripped, client.Spec(), first.EvidenceHash, "jev-latest", first.BatchSemantics)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Reusable) != 0 {
		t.Fatalf("a run without an evidence hash must not be reused: %+v", plan)
	}
	// A partial previous run leaves the unanswered question to inference.
	partial := first
	partial.Coverage = "partial"
	delete(partial.Judgments, "topic_llm")
	delete(partial.QuestionHashes, "topic_llm")
	plan, err = PlanReuse(&partial, client.Spec(), first.EvidenceHash, "jev-latest", first.BatchSemantics)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToInfer) != 1 || plan.ToInfer[0] != "topic_llm" {
		t.Fatalf("an unanswered question must be inferred: %+v", plan)
	}
}

func TestEvaluateReusingRefusesToMixModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	previous := RawJudgments{
		SpecID: "classify-v1", ResolvedModel: "jev-pinned-old", RequestedModel: "jev-pinned-old",
		EvidenceHash: "e", BatchSemantics: "batch-1", QuestionHashes: map[string]string{}, Judgments: map[string]RawJudgment{},
	}
	if _, err := client.EvaluateReusing(context.Background(), Input{OriginalText: "text"}, &previous, "batch-1"); !errors.Is(err, ErrReuseUnsafe) {
		t.Fatalf("mixing runs from another resolved model must fail explicitly, got %v", err)
	}
}

// TestQuestionCacheKeySeparatesSemantics is the conservative cache rule: the
// same question ID with different bytes must never share a key.
func TestQuestionCacheKeySeparatesSemantics(t *testing.T) {
	spec, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	first := spec.Questions[0]
	second := first
	second.Criteria = mustJSON(map[string]string{"true": "changed", "false": "changed"})
	firstHash, err := QuestionHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := QuestionHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("a criteria change must change the question hash")
	}
	if QuestionCacheKey("e", firstHash, "m", "b") == QuestionCacheKey("e", secondHash, "m", "b") {
		t.Fatal("different question semantics must not share a cache key")
	}
	if QuestionCacheKey("e", firstHash, "m", "b") == QuestionCacheKey("e2", firstHash, "m", "b") {
		t.Fatal("different evidence must not share a cache key")
	}
	if QuestionCacheKey("e", firstHash, "m", "b") == QuestionCacheKey("e", firstHash, "m2", "b") {
		t.Fatal("different models must not share a cache key")
	}
	// The display label is not part of a question, so a rename keeps the hash.
	renamed := first
	if renamedHash, _ := QuestionHash(renamed); renamedHash != firstHash {
		t.Fatal("hashing must be deterministic")
	}
}
