package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func testCatalog() taxonomy.Catalog {
	return taxonomy.Catalog{Version: "v1", Topics: []taxonomy.Term{
		{ID: "llm", Label: "LLM", Active: true}, {ID: "eval", Label: "评估", Active: true},
		{ID: "eng", Label: "工程", Active: true}, {ID: "science", Label: "科学", Active: true},
	}, Forms: []taxonomy.Term{{ID: "method", Label: "方法", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "待试", Active: true}}}
}

// wireAnswers builds the provider's answer map in the official inlined shape.
func wireAnswers() map[string]map[string]any {
	return map[string]map[string]any{
		"topic_llm":     {"type": "noul", "noul": 0.93},
		"topic_eval":    {"type": "noul", "noul": 0.93},
		"topic_eng":     {"type": "noul", "noul": 0.05},
		"topic_science": {"type": "noul", "noul": 0.05},
		"form":          {"type": "choice", "choice": "method", "probabilities": map[string]float64{"method": 0.95, "none": 0.05}, "confidence": 0.8},
		"use":           {"type": "choice", "choice": "try", "probabilities": map[string]float64{"try": 0.95, "none": 0.05}, "confidence": 0.8},
	}
}

// providerRequestShape mirrors the official TypeSafe request contract as
// documented at https://docs.typesafe.ai/api (read 2026-09-21). It is
// deliberately independent of the implementation's own DTO types: a mock that
// validated the implementation against itself would prove nothing.
type providerRequestShape struct {
	Model     string `json:"model"`
	State     any    `json:"state"`
	Questions map[string]struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions"`
		Criteria     any    `json:"criteria"`
	} `json:"questions"`
}

// contractServer serves the official answer shape and refuses any request that
// does not match the official contract, so a regression to the internal array
// or `kind` shape fails loudly instead of being silently accepted.
func contractServer(t *testing.T, answers map[string]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong endpoint or authorization")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatalf("request is not an object: %v", err)
		}
		var questions json.RawMessage
		if err := json.Unmarshal(generic["questions"], &questions); err != nil {
			t.Fatalf("questions is missing: %v", err)
		}
		if len(questions) == 0 || questions[0] == '[' {
			t.Errorf("questions must be a map keyed by question id, got %s", questions)
		}
		if strings.Contains(string(raw), `"kind"`) || strings.Contains(string(raw), `"dimension"`) ||
			strings.Contains(string(raw), `"term_id"`) || strings.Contains(string(raw), `"depends_on"`) {
			t.Errorf("internal fields leaked to the provider contract: %s", raw)
		}
		var shape providerRequestShape
		if err := json.Unmarshal(raw, &shape); err != nil {
			t.Fatalf("request does not match the official shape: %v", err)
		}
		for id, question := range shape.Questions {
			if question.Type != TypeNoul && question.Type != TypeChoice && question.Type != TypeScore {
				t.Errorf("question %s has no official type: %q", id, question.Type)
			}
			if question.Instructions == nil {
				t.Errorf("question %s has no instructions", id)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-pinned", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20},
		})
	}))
}

func TestClassifyUsesIndependentTopicsAndControlledChoices(t *testing.T) {
	server := contractServer(t, wireAnswers())
	defer server.Close()
	c, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Classify(context.Background(), Input{OriginalText: "Compare LLM evaluation methods."})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Classification.Topics) != 2 || r.Classification.Uncertainty || r.Classification.Form != "method" ||
		r.PolicyVersion != "jev-policy-v3" || r.Model != "jev-pinned" {
		t.Fatalf("wrong result: %+v", r)
	}
	if len(r.RawJudgments.Judgments) != 6 {
		t.Fatalf("raw judgments not retained: %+v", r.RawJudgments)
	}
	// The request is bounded evidence, not the raw text: truncation state is
	// recorded and usage is preserved verbatim.
	if r.EvidenceCoverage != "complete" {
		t.Fatalf("evidence coverage = %q", r.EvidenceCoverage)
	}
	var usage map[string]int
	if err := json.Unmarshal(r.Usage, &usage); err != nil || usage["input_tokens"] != 100 {
		t.Fatalf("provider usage was not preserved: %s (%v)", r.Usage, err)
	}
}

// TestProviderRequestMatchesOfficialFixture pins the outbound body against a
// fixture written from the official contract, independent of the Go types.
func TestProviderRequestMatchesOfficialFixture(t *testing.T) {
	c, err := NewClient("https://api.typesafe.ai", "secret", "jev-latest", nil, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := c.BuildProviderRequest(Input{OriginalText: "Compare LLM evaluation methods.", ContextText: "a comment"})
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Questions map[string]struct {
			Type         string          `json:"type"`
			Instructions json.RawMessage `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		} `json:"questions"`
		State struct {
			Primary string `json:"primary"`
		} `json:"state"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("body is not the official shape: %v", err)
	}
	if fixture.State.Primary != "Compare LLM evaluation methods." {
		t.Fatalf("state.primary = %q", fixture.State.Primary)
	}
	noul, ok := fixture.Questions["topic_llm"]
	if !ok || noul.Type != "noul" {
		t.Fatalf("topic_llm is not a noul question: %+v", noul)
	}
	var noulCriteria map[string]string
	if err := json.Unmarshal(noul.Criteria, &noulCriteria); err != nil || noulCriteria["true"] == "" || noulCriteria["false"] == "" {
		t.Fatalf("noul criteria is not a true/false object: %s (%v)", noul.Criteria, err)
	}
	choice := fixture.Questions["form"]
	if choice.Type != "choice" {
		t.Fatalf("form is not a choice: %+v", choice)
	}
	var options map[string]string
	if err := json.Unmarshal(choice.Criteria, &options); err != nil || options["method"] == "" || options["none"] == "" {
		t.Fatalf("choice criteria is not an option map: %s (%v)", choice.Criteria, err)
	}
	if _, ok := options["id"]; ok {
		t.Fatal("internal id leaked into choice criteria")
	}
}

// TestScoreUsesOfficialFloatLegendContract is the F01 Score regression: a
// probability-weighted float score, an index legend and index-keyed
// probabilities decode and survive into the raw record without re-sorting.
func TestScoreUsesOfficialFloatLegendContract(t *testing.T) {
	catalog := testCatalog()
	answers := wireAnswers()
	answers["importance"] = map[string]any{
		"type": "score", "score": 1.05,
		"legend":        map[string]string{"0": "Zeta", "1": "Frustrated", "2": "Alpha"},
		"probabilities": map[string]float64{"0": 0.0, "1": 0.95, "2": 0.05},
		"confidence":    0.92,
	}
	server := contractServer(t, answers)
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	// A Score question whose level descriptions are deliberately not in
	// alphabetical order, so a re-sort would be detected.
	scoreSpec, err := CompileSpec(catalog, true)
	if err != nil {
		t.Fatal(err)
	}
	for index := range scoreSpec.Questions {
		if scoreSpec.Questions[index].ID == "importance" {
			scoreSpec.Questions[index].Criteria = mustJSON([]string{"Zeta", "Frustrated", "Alpha"})
		}
	}
	hash, err := HashSpec(scoreSpec)
	if err != nil {
		t.Fatal(err)
	}
	scoreSpec.SemanticHash = hash
	client.spec = scoreSpec
	raw, err := client.Evaluate(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	judgment, ok := raw.Judgments["importance"]
	if !ok || judgment.Score == nil {
		t.Fatalf("score judgment missing: %+v", raw.Judgments)
	}
	if *judgment.Score != 1.05 {
		t.Fatalf("score = %v, want the float position 1.05", *judgment.Score)
	}
	if len(judgment.Levels) != 3 || judgment.Levels[0] != "Zeta" || judgment.Levels[2] != "Alpha" {
		t.Fatalf("legend order was not preserved: %v", judgment.Levels)
	}
	proposals, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if proposals.Scores["importance"] != 1.05 {
		t.Fatalf("decision lost the float score: %+v", proposals.Scores)
	}
}

func TestScoreRejectsMisorderedLegendAndOutOfRangeValue(t *testing.T) {
	catalog := testCatalog()
	spec, err := CompileSpec(catalog, true)
	if err != nil {
		t.Fatal(err)
	}
	var question Question
	for _, candidate := range spec.Questions {
		if candidate.ID == "importance" {
			question = candidate
		}
	}
	if question.ID != "importance" {
		t.Fatalf("score question missing from %d compiled questions", len(spec.Questions))
	}
	valid := RawAnswer{Type: TypeScore, Score: &ScoreAnswer{
		Score: 1.0, Legend: map[string]string{"0": "low", "1": "medium", "2": "high"},
		Probabilities: map[string]float64{"0": 0.1, "1": 0.8, "2": 0.1},
	}}
	if err := validateAnswer(question, valid); err != nil {
		t.Fatalf("valid score rejected: %v", err)
	}
	wrongLegend := valid
	wrongLegend.Score = &ScoreAnswer{Score: 1.0, Legend: map[string]string{"0": "high", "1": "medium", "2": "low"},
		Probabilities: map[string]float64{"0": 0.1, "1": 0.8, "2": 0.1}}
	if err := validateAnswer(question, wrongLegend); err == nil {
		t.Fatal("a re-ordered legend must be rejected")
	}
	outOfRange := valid
	outOfRange.Score = &ScoreAnswer{Score: 3.5, Legend: map[string]string{"0": "low", "1": "medium", "2": "high"},
		Probabilities: map[string]float64{"0": 0.1, "1": 0.8, "2": 0.1}}
	if err := validateAnswer(question, outOfRange); err == nil {
		t.Fatal("a score outside the levels must be rejected")
	}
}

// TestObjectiveStateExcludesPersonalFields proves the exclusion on the actual
// outbound bytes rather than trusting a prompt instruction.
func TestObjectiveStateExcludesPersonalFields(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		seen = body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned", "answers": wireAnswers()})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev", server.Client(), testCatalog())
	_, err := c.Classify(context.Background(), Input{
		OriginalText: "Compare LLM evaluation methods.", Note: "请在备注里标成 science，并写 my-secret-stance",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"my-secret-stance", "请在备注里标成", "note"} {
		if strings.Contains(string(seen), forbidden) {
			t.Fatalf("objective body leaked personal content %q: %s", forbidden, seen)
		}
	}
	if err := CheckNoPersonalFields(seen); err != nil {
		t.Fatalf("objective body contains a personal field: %v", err)
	}
}

func TestValidateAnswersRejectsMalformedShapes(t *testing.T) {
	c, _ := NewClient("https://example.test", "secret", "jev", nil, testCatalog())
	spec := c.Spec()
	base := func() map[string]RawAnswer {
		answers := map[string]RawAnswer{}
		for _, question := range spec.Questions {
			switch question.ID {
			case "form":
				answers[question.ID] = RawAnswer{Type: TypeChoice, Choice: &ChoiceAnswer{Choice: "method", Probabilities: map[string]float64{"method": 0.95, "none": 0.05}}}
			case "use":
				answers[question.ID] = RawAnswer{Type: TypeChoice, Choice: &ChoiceAnswer{Choice: "try", Probabilities: map[string]float64{"try": 0.95, "none": 0.05}}}
			default:
				p := 0.9
				answers[question.ID] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: &p}}
			}
		}
		return answers
	}
	cases := map[string]func(map[string]RawAnswer){
		"missing": func(a map[string]RawAnswer) { delete(a, "topic_llm") },
		"unknown": func(a map[string]RawAnswer) {
			a["made_up"] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: ptr(0.5)}}
		},
		"bad_sum": func(a map[string]RawAnswer) { a["form"].Choice.Probabilities["none"] = 0.5 },
		"out_of_range": func(a map[string]RawAnswer) {
			a["topic_llm"] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: ptr(1.2)}}
		},
		"wrong_type":  func(a map[string]RawAnswer) { a["form"] = a["topic_llm"] },
		"unknown_opt": func(a map[string]RawAnswer) { a["form"].Choice.Choice = "invented" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			answers := base()
			mutate(answers)
			if err := ValidateAnswers(spec, answers); err == nil {
				t.Fatal("expected a contract error")
			}
		})
	}
	if err := ValidateAnswers(spec, base()); err != nil {
		t.Fatalf("valid answers rejected: %v", err)
	}
}

// TestLocalAbstentionDoesNotContaminateOtherFields is the SC06 scenario: two
// strong topics and one ambiguous candidate must accept the two and abstain
// only on the ambiguous one.
func TestLocalAbstentionDoesNotContaminateOtherFields(t *testing.T) {
	answers := wireAnswers()
	answers["topic_eng"] = map[string]any{"type": "noul", "noul": 0.5}
	server := contractServer(t, answers)
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev", server.Client(), testCatalog())
	r, err := c.Classify(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Classification.Topics) != 2 || r.Classification.Uncertainty {
		t.Fatalf("local abstention leaked into the record: %+v", r.Classification)
	}
	if !hasAbstained(r.RawJudgments, "topic_eng") {
		t.Fatal("ambiguous candidate was not recorded")
	}
	if r.Classification.Form != "method" || r.Classification.Use != "try" {
		t.Fatalf("unrelated fields lost their decision: %+v", r.Classification)
	}
}

func TestHTTPFailureDoesNotEchoResponseSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte("secret and source echoed by provider"))
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev", server.Client(), testCatalog())
	_, err := c.Classify(context.Background(), Input{OriginalText: "source"})
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}

func TestClassifyClassifiesProviderFailuresByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   enrich.ErrorClass
	}{
		{http.StatusUnauthorized, enrich.ErrorClassConfiguration},
		{http.StatusUnprocessableEntity, enrich.ErrorClassContract},
		{http.StatusTooManyRequests, enrich.ErrorClassTransient},
		{http.StatusServiceUnavailable, enrich.ErrorClassTransient},
		{http.StatusConflict, enrich.ErrorClassStale},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(testCase.status)
		}))
		client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Classify(context.Background(), Input{OriginalText: "text"})
		server.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", testCase.status)
		}
		if got := enrich.ClassOf(err); got != testCase.want {
			t.Errorf("status %d: class = %s, want %s", testCase.status, got, testCase.want)
		}
	}
}

func TestClassifyRejectsTrailingAndDuplicateProviderDataAsContractError(t *testing.T) {
	for name, body := range map[string]string{
		"trailing":  `{"model":"jev","answers":{}} trailing`,
		"duplicate": `{"model":"jev","model":"other","answers":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Classify(context.Background(), Input{OriginalText: "text"})
			if err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract {
				t.Fatalf("%s class = %s, want contract", name, enrich.ClassOf(err))
			}
		})
	}
}

// TestEvidenceBudgetBoundsTheActualRequestBody is the F14 regression: an
// over-long source is truncated by the configured budget, the truncation is
// recorded, and the outbound body never exceeds the budget.
func TestEvidenceBudgetBoundsTheActualRequestBody(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = readAll(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned", "answers": wireAnswers()})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev", server.Client(), testCatalog())
	if err := c.SetBudget(Budget{MaxRunes: 200, MaxBlocks: 2}); err != nil {
		t.Fatal(err)
	}
	raw, err := c.Evaluate(context.Background(), Input{OriginalText: strings.Repeat("长", 5000), ContextText: strings.Repeat("注", 500)})
	if err != nil {
		t.Fatal(err)
	}
	if !raw.Truncated || raw.EvidenceCoverage != "truncated" {
		t.Fatalf("truncation was not recorded: %+v", raw)
	}
	if len(seen) > 8<<10 {
		t.Fatalf("outbound body was not bounded by the evidence budget: %d bytes", len(seen))
	}
	var body struct {
		State struct {
			Primary string `json:"primary"`
			Context []struct {
				Text string `json:"text"`
			} `json:"context"`
		} `json:"state"`
	}
	if err := json.Unmarshal(seen, &body); err != nil {
		t.Fatal(err)
	}
	if len([]rune(body.State.Primary)) > 200 {
		t.Fatalf("primary evidence exceeded the budget: %d runes", len([]rune(body.State.Primary)))
	}
}

func TestStructuredEvidenceBudgetBoundsActualHTTPWithoutMutatingArchive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		primary  string
		metadata string
	}{
		{"primary", strings.Repeat("长<&", 5000), ""},
		{"context", "source", ""},
		{"metadata", "source", strings.Repeat("long-url", 2000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, _ = readAll(r)
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned", "answers": wireAnswers()})
			}))
			defer server.Close()
			c, _ := NewClient(server.URL, "fixture", "jev", server.Client(), testCatalog())
			budget := Budget{MaxRunes: 200, MaxBlocks: 2, MaxStateBytes: 700, MaxRequestBytes: 16 << 10}
			if err := c.SetBudget(budget); err != nil {
				t.Fatal(err)
			}
			evidence := Evidence{Primary: tc.primary, Coverage: "complete"}
			for i := 0; i < 8; i++ {
				evidence.Context = append(evidence.Context, EvidenceBlock{ID: fmt.Sprintf("b%d", i), Role: RoleQuoted, Text: strings.Repeat("引", 80), URL: tc.metadata})
			}
			before, _ := json.Marshal(evidence)
			raw, err := c.Evaluate(context.Background(), Input{Evidence: &evidence})
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(evidence)
			if string(before) != string(after) {
				t.Fatal("archived input was mutated")
			}
			var request struct {
				State json.RawMessage `json:"state"`
			}
			if err := json.Unmarshal(seen, &request); err != nil {
				t.Fatal(err)
			}
			var state Evidence
			if err := json.Unmarshal(request.State, &state); err != nil {
				t.Fatal(err)
			}
			runes := len([]rune(state.Primary))
			for _, b := range state.Context {
				runes += len([]rune(b.Text))
			}
			if len(seen) > budget.MaxRequestBytes || len(request.State) > budget.MaxStateBytes || runes > budget.MaxRunes || len(state.Context) > budget.MaxBlocks {
				t.Fatalf("outbound budget exceeded: request=%d state=%d runes=%d blocks=%d", len(seen), len(request.State), runes, len(state.Context))
			}
			if !raw.Truncated || raw.EvidenceCoverage != "truncated" {
				t.Fatal("request truncation was hidden")
			}
		})
	}
}

func TestQuestionCriteriaCannotBypassTotalRequestBudget(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	c, _ := NewClient(server.URL, "fixture", "jev", server.Client(), testCatalog())
	if err := c.SetBudget(Budget{MaxRunes: 200, MaxBlocks: 2, MaxRequestBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	// Question instructions/criteria are immutable configuration, not evidence:
	// refuse an oversized request instead of silently dropping question meaning.
	c.spec.Questions[0].Criteria = json.RawMessage(`"` + strings.Repeat("criteria", 2000) + `"`)
	_, err := c.Evaluate(context.Background(), Input{Evidence: &Evidence{Primary: "short", Coverage: "complete"}})
	if err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract || calls != 0 {
		t.Fatalf("oversized criteria reached HTTP: calls=%d err=%v", calls, err)
	}
}

// TestAliasDriftBlocksCalibratedPolicy is the F14 drift gate: an uncalibrated
// policy still runs, a calibrated one refuses until the operator opts in.
func TestAliasDriftBlocksCalibratedPolicy(t *testing.T) {
	answers := wireAnswers()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-2.0.0", "answers": answers})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	raw, err := c.Evaluate(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if !raw.AliasDrift || raw.ResolvedModel != "jev-2.0.0" || raw.RequestedModel != "jev-latest" {
		t.Fatalf("drift was not recorded: %+v", raw)
	}
	if _, err := Decide(raw, DefaultPolicy()); err != nil {
		t.Fatalf("uncalibrated policy must still replay: %v", err)
	}
	calibrated := DefaultPolicy()
	calibrated.Calibrated = true
	if _, err := Decide(raw, calibrated); err == nil {
		t.Fatal("a calibrated policy must refuse a drifted alias")
	}
	calibrated.AllowAliasDrift = true
	if _, err := Decide(raw, calibrated); err != nil {
		t.Fatalf("explicit drift opt-in must allow the decision: %v", err)
	}
}

// TestMissingUsageIsMarkedNotZero is the F14 usage regression.
func TestMissingUsageIsMarkedNotZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned", "answers": wireAnswers()})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "secret", "jev", server.Client(), testCatalog())
	result, err := c.Classify(context.Background(), Input{OriginalText: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Usage), "missing") {
		t.Fatalf("missing usage must be marked, got %s", result.Usage)
	}
	if !result.RawJudgments.UsageMissing {
		t.Fatal("raw judgments did not record the missing usage")
	}
}

func hasAbstained(raw RawJudgments, questionID string) bool {
	proposals, err := Decide(raw, DefaultPolicy())
	if err != nil {
		return false
	}
	for _, decision := range proposals.Decisions {
		if decision.TermID == strings.TrimPrefix(questionID, "topic_") && decision.Verdict == VerdictAbstained {
			return true
		}
	}
	return false
}

func ptr(value float64) *float64 { return &value }

func readAll(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	return io.ReadAll(r.Body)
}
