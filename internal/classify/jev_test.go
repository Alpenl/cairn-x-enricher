package classify

import (
	"context"
	"encoding/json"
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

// wireAnswers builds the provider's answer map in the inlined shape.
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

func serveAnswers(t *testing.T, answers map[string]map[string]any, check func(wireRequest)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong endpoint or authorization")
		}
		var req wireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if check != nil {
			check(req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-pinned", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20},
		})
	}))
}

func TestClassifyUsesIndependentTopicsAndControlledChoices(t *testing.T) {
	server := serveAnswers(t, wireAnswers(), func(req wireRequest) {
		if req.State.OriginalText != "Compare LLM evaluation methods." {
			t.Errorf("wrong state: %+v", req.State)
		}
		// Questions are an ordered array, not a map keyed by ID.
		kinds := map[string]QuestionKind{}
		for _, question := range req.Questions {
			kinds[question.ID] = question.Kind
		}
		if kinds["topic_eval"] != QuestionNoul || kinds["form"] != QuestionChoice {
			t.Errorf("wrong compiled question kinds: %+v", kinds)
		}
	})
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
		r.PolicyVersion != "jev-policy-v2" || r.Model != "jev-pinned" {
		t.Fatalf("wrong result: %+v", r)
	}
	if len(r.RawJudgments.Judgments) != 6 {
		t.Fatalf("raw judgments not retained: %+v", r.RawJudgments)
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
	server := serveAnswers(t, answers, nil)
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
	// The form/use answers are still decided.
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
