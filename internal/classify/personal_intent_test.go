package classify

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestObjectiveClassificationCannotInferPersonalOpposition(t *testing.T) {
	catalog := testCatalog()
	catalog.Uses = append(catalog.Uses, taxonomy.Term{ID: "contra", Label: "反对", Active: true, Description: "收藏备注明确表明用户反对该观点，不能自行推断用户立场。"})
	answers := wireAnswers()
	answers["use"] = map[string]any{"type": "choice", "choice": "contra", "probabilities": map[string]float64{"contra": 0.98, "try": 0.01, "none": 0.01}, "confidence": 0.95}
	server := contractServer(t, answers)
	defer server.Close()
	client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), Input{OriginalText: "This source compares two evaluation methods. It says nothing about the reader's position."})
	// Rejecting a provider option outside the objective spec is also safe.
	if err != nil {
		if enrich.ClassOf(err) != enrich.ErrorClassContract || !strings.Contains(err.Error(), `choice "contra" is not an option of use`) {
			t.Fatalf("unexpected error masks stance guard: %v", err)
		}
		return
	}
	if result.Classification.Use == "contra" {
		t.Fatal("objective request without a user statement inferred personal opposition")
	}
	proposals, err := Decide(result.RawJudgments, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if proposals.Use == "contra" {
		t.Fatal("offline objective replay inferred personal opposition")
	}
}

func TestObjectiveReplayCannotInferPersonalOpposition(t *testing.T) {
	raw := completeRaw(rawNoul("llm", 0.9), rawChoice("form", "method", map[string]float64{"method": 0.9, "none": 0.1}), rawChoice("use", "contra", map[string]float64{"contra": 0.98, "none": 0.02}))
	proposals, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if proposals.Use == "contra" {
		t.Fatal("objective replay without explicit human input inferred personal opposition")
	}
}

func TestPersonalUsePolicyPreservesHistoricalReplayAndRaw(t *testing.T) {
	raw := completeRaw(rawNoul("llm", .9), rawChoice("form", "method", map[string]float64{"method": .9, "none": .1}), rawChoice("use", "contra", map[string]float64{"contra": .98, "none": .02}))
	before, _ := json.Marshal(raw)
	legacy := DefaultPolicy()
	legacy.Version = "jev-policy-v2"
	legacy.BlockPersonalUse = false
	saved, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "block_personal_use") {
		t.Fatal("changed historical policy bytes")
	}
	decoded, err := DecodePolicy(saved)
	if err != nil {
		t.Fatal(err)
	}
	old, err := Decide(raw, decoded)
	if err != nil {
		t.Fatal(err)
	}
	if old.Use != "contra" {
		t.Fatal("silently changed historical policy replay")
	}
	next, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionFor(next, "use", "")
	if next.Use != "" || decision.Verdict != VerdictAbstained || decision.Candidate != "contra" || decision.Probability != .98 || !strings.Contains(decision.Reason, "explicit human") {
		t.Fatalf("lost safe decision or audit detail: %+v", decision)
	}
	after, _ := json.Marshal(raw)
	if string(before) != string(after) {
		t.Fatal("modified historical raw")
	}
	legacy.BlockPersonalUse = true
	if legacy.Validate() == nil {
		t.Fatal("allowed changed semantics under historical identity")
	}
	invalid := DefaultPolicy()
	invalid.BlockPersonalUse = false
	if invalid.Validate() == nil {
		t.Fatal("allowed guard downgrade under current identity")
	}
}

func TestObjectiveUseSpecAndNoteIsolation(t *testing.T) {
	previousBytes, err := os.ReadFile("../../experiments/classification/reference-v1/source-roles-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := DecodeSpec(previousBytes)
	if err != nil {
		t.Fatal(err)
	}
	objectiveBytes, err := os.ReadFile("../../experiments/classification/reference-v1/objective-use-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	objective, err := DecodeSpec(objectiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient("https://offline.invalid", "fixture", "jev-1.13.0", nil, frozenFieldCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	current := client.Spec()
	if objective.SpecID == previous.SpecID || len(objective.Questions) != len(previous.Questions) {
		t.Fatal("wrong objective use spec identity/population")
	}
	for i, q := range objective.Questions {
		if q.ID != "use" {
			if !reflect.DeepEqual(q, previous.Questions[i]) {
				t.Fatalf("unrelated question changed: %s", q.ID)
			}
			continue
		}
		if containsString(q.AnswerOptions(), "contra") || !containsString(previous.Questions[i].AnswerOptions(), "contra") {
			t.Fatal("personal stance still objective candidate or historical baseline changed")
		}
		if strings.Contains(string(q.Instructions), "收藏备注") {
			t.Fatal("objective use still requires unavailable note")
		}
		if !reflect.DeepEqual(q, current.Questions[i]) {
			t.Fatal("current use changed from the frozen objective-only question")
		}
	}
	a, _, err := client.BuildProviderRequest(Input{OriginalText: "An objective method."})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := client.BuildProviderRequest(Input{OriginalText: "An objective method.", Note: "private-user-stance-opposes-this"})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("personal note changed objective request")
	}
}

func TestObjectiveUseRequiresAnActiveNonPersonalCandidate(t *testing.T) {
	catalog := testCatalog()
	catalog.Uses = []taxonomy.Term{{ID: "contra", Label: "Personal opposition", Active: true}}
	_, err := NewClient("https://offline.invalid", "fixture", "jev-1.13.0", nil, catalog)
	if err == nil || !strings.Contains(err.Error(), "choice question use must define at least two options") {
		t.Fatalf("unsupported objective vocabulary must fail before network: %v", err)
	}
}
