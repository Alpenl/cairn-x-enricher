package classify

import (
	"context"
	"encoding/json"
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

func answers() map[string]Answer {
	one, low, confidence := 0.93, 0.05, 0.8
	return map[string]Answer{
		"topic_llm": {Type: "noul", Noul: &one}, "topic_eval": {Type: "noul", Noul: &one},
		"topic_eng": {Type: "noul", Noul: &low}, "topic_science": {Type: "noul", Noul: &low},
		"form": {Type: "choice", Choice: "method", Probabilities: map[string]float64{"method": 0.95, "none": 0.05}, Confidence: &confidence},
		"use":  {Type: "choice", Choice: "try", Probabilities: map[string]float64{"try": 0.95, "none": 0.05}, Confidence: &confidence},
	}
}

func TestClassifyUsesIndependentTopicsAndControlledChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong endpoint or authorization")
		}
		var req struct {
			State     Input               `json:"state"`
			Questions map[string]question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.State.OriginalText != "Compare LLM evaluation methods." || req.Questions["topic_eval"].Type != "noul" || req.Questions["form"].Type != "choice" {
			t.Errorf("wrong semantic request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-pinned", "answers": answers(), "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}})
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Classify(context.Background(), Input{OriginalText: "Compare LLM evaluation methods."})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Classification.Topics) != 2 || r.Classification.Uncertainty || r.Classification.Form != "method" || r.PolicyVersion != PolicyVersion || r.Model != "jev-pinned" {
		t.Fatalf("wrong result: %+v", r)
	}
}

func TestSelectionRejectsMalformedAnswersAndMarksAmbiguity(t *testing.T) {
	c, _ := NewClient("https://example.test", "secret", "jev", nil, testCatalog())
	for _, kind := range []string{"missing", "unknown", "not_maximum", "bad_sum", "missing_noul", "out_of_range"} {
		t.Run(kind, func(t *testing.T) {
			a := answers()
			switch kind {
			case "missing":
				delete(a, "topic_llm")
			case "unknown":
				v := a["form"]
				v.Choice = "invented"
				a["form"] = v
			case "not_maximum":
				v := a["form"]
				v.Choice = "none"
				a["form"] = v
			case "bad_sum":
				a["form"].Probabilities["none"] = 0.5
			case "missing_noul":
				a["topic_llm"] = Answer{Type: "noul"}
			case "out_of_range":
				v := 1.2
				a["topic_llm"] = Answer{Type: "noul", Noul: &v}
			}
			if _, err := c.selectAnswers(a); err == nil {
				t.Fatal("expected invalid answer error")
			}
		})
	}
	a := answers()
	mid := 0.5
	a["topic_eng"] = Answer{Type: "noul", Noul: &mid}
	choice := a["use"]
	choice.Probabilities = map[string]float64{"try": 0.51, "none": 0.49}
	a["use"] = choice
	r, err := c.selectAnswers(a)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Uncertainty || r.Use != "" || len(r.Topics) != 2 {
		t.Fatalf("ambiguous answers must not become definite tags: %+v", r)
	}
	for key, v := range a {
		if v.Type == "noul" {
			p := 0.99
			v.Noul = &p
			a[key] = v
		}
	}
	r, err = c.selectAnswers(a)
	if err != nil || len(r.Topics) != 3 || !r.Uncertainty {
		t.Fatalf("top-three overflow: %+v %v", r, err)
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

func TestClassifyRejectsTrailingProviderDataAsContractError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev","answers":{}} trailing`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), Input{OriginalText: "text"})
	if err == nil || enrich.ClassOf(err) != enrich.ErrorClassContract {
		t.Fatalf("trailing data class = %s, want contract", enrich.ClassOf(err))
	}
}
