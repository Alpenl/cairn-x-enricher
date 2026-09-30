package classify

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestRejectsInvalidConfidenceBeforePersistingJudgments(t *testing.T) {
	question := Question{ID: "form", Kind: QuestionChoice, Criteria: mustJSON(map[string]string{"a": "A", "b": "B"})}
	for _, confidence := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		answer := RawAnswer{Type: TypeChoice, Confidence: &confidence, Choice: &ChoiceAnswer{Choice: "a", Probabilities: map[string]float64{"a": .8, "b": .2}}}
		if err := validateAnswer(question, answer); err == nil {
			t.Errorf("accepted invalid confidence %v", confidence)
		}
	}
}

func TestEvaluateRejectsInvalidWireConfidence(t *testing.T) {
	for _, confidence := range []float64{-.1, 1.1} {
		answers := wireAnswers()
		answers["form"]["confidence"] = confidence
		server := contractServer(t, answers)
		client, err := NewClient(server.URL, "secret", "jev-latest", server.Client(), testCatalog())
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = client.Evaluate(context.Background(), Input{OriginalText: "A reproducible method."})
		server.Close()
		if enrich.ClassOf(err) != enrich.ErrorClassContract || !strings.Contains(err.Error(), "confidence") {
			t.Fatalf("invalid confidence did not fail at actual HTTP contract: %v", err)
		}
	}
}

func TestRejectsNonfiniteScoreBeforePersistingJudgments(t *testing.T) {
	question := Question{ID: "importance", Kind: QuestionScore, Criteria: mustJSON([]string{"no actionable steps", "some steps missing", "complete reproducible procedure"})}
	for _, score := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		answer := RawAnswer{Type: TypeScore, Score: &ScoreAnswer{Score: score, Legend: question.ScoreLegend(), Probabilities: map[string]float64{"0": .2, "1": .6, "2": .2}}}
		if err := validateAnswer(question, answer); err == nil {
			t.Errorf("accepted nonfinite Score %v", score)
		}
	}
}
