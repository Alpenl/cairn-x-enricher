package classify

import (
	"errors"
	"math"
	"testing"
)

// rawNoul builds a raw judgment for a topic.
func rawNoul(term string, p float64) RawJudgment {
	return RawJudgment{QuestionID: "topic_" + term, Kind: QuestionNoul, Dimension: "topic", TermID: term, Noul: &p}
}

func rawChoice(dimension, value string, distribution map[string]float64) RawJudgment {
	return RawJudgment{QuestionID: dimension, Kind: QuestionChoice, Dimension: dimension, Choice: value, Probabilities: distribution}
}

func rawScore(questionID string, score float64, levels []string, distribution map[string]float64) RawJudgment {
	return RawJudgment{QuestionID: questionID, Kind: QuestionScore, Dimension: "importance", Score: &score, Levels: levels, Probabilities: distribution}
}

func completeRaw(judgments ...RawJudgment) RawJudgments {
	byID := map[string]RawJudgment{}
	for _, judgment := range judgments {
		byID[judgment.QuestionID] = judgment
	}
	return RawJudgments{SpecID: "classify-v1", TaxonomyVersion: "v1", RequestedModel: "jev", ResolvedModel: "jev-pinned", Judgments: byID, Coverage: "complete"}
}

func decisionFor(proposals Proposals, dimension, term string) FieldDecision {
	wanted := normalizeDimension(dimension)
	for _, decision := range proposals.Decisions {
		if decision.Dimension == wanted && decision.TermID == term {
			return decision
		}
	}
	return FieldDecision{}
}

// --- Policy table-driven scenarios -----------------------------------------

func TestDecidePolicyScenarios(t *testing.T) {
	policy := DefaultPolicy()
	cases := []struct {
		name       string
		raw        RawJudgments
		wantTopics []string
		wantForm   string
		wantUse    string
		abstained  []string
		rejected   []string
	}{
		{
			name: "two strong and one ambiguous topic abstain locally",
			raw: completeRaw(
				rawNoul("llm", 0.95), rawNoul("eval", 0.9), rawNoul("eng", 0.5), rawNoul("science", 0.05),
				rawChoice("form", "method", map[string]float64{"method": 0.95, "none": 0.05}),
				rawChoice("use", "try", map[string]float64{"try": 0.95, "none": 0.05}),
			),
			wantTopics: []string{"llm", "eval"}, wantForm: "method", wantUse: "try",
			abstained: []string{"eng"}, rejected: []string{"science"},
		},
		{
			name: "all topics low with complete evidence is a legal empty result",
			raw: completeRaw(
				rawNoul("llm", 0.05), rawNoul("eval", 0.05), rawNoul("eng", 0.05), rawNoul("science", 0.05),
				rawChoice("form", "none", map[string]float64{"method": 0.06, "none": 0.94}),
				rawChoice("use", "none", map[string]float64{"try": 0.06, "none": 0.94}),
			),
			wantTopics: []string{}, wantForm: "", wantUse: "",
			rejected: []string{"llm", "eval", "eng", "science"},
		},
		{
			name: "four strong topics are all retained underneath",
			raw: completeRaw(
				rawNoul("llm", 0.95), rawNoul("eval", 0.94), rawNoul("eng", 0.93), rawNoul("science", 0.92),
				rawChoice("form", "method", map[string]float64{"method": 0.95, "none": 0.05}),
				rawChoice("use", "try", map[string]float64{"try": 0.95, "none": 0.05}),
			),
			wantTopics: []string{"llm", "eval", "eng", "science"}, wantForm: "method", wantUse: "try",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			proposals, err := Decide(testCase.raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(proposals.Topics, testCase.wantTopics) {
				t.Fatalf("topics = %v, want %v", proposals.Topics, testCase.wantTopics)
			}
			if proposals.Form != testCase.wantForm || proposals.Use != testCase.wantUse {
				t.Fatalf("form/use = %q/%q, want %q/%q", proposals.Form, proposals.Use, testCase.wantForm, testCase.wantUse)
			}
			for _, term := range testCase.abstained {
				if decisionFor(proposals, "topic", term).Verdict != VerdictAbstained {
					t.Errorf("%s should abstain, got %+v", term, decisionFor(proposals, "topic", term))
				}
			}
			for _, term := range testCase.rejected {
				if decisionFor(proposals, "topic", term).Verdict != VerdictRejected {
					t.Errorf("%s should be rejected, got %+v", term, decisionFor(proposals, "topic", term))
				}
			}
		})
	}
}

func TestDecideRejectsUnknownCoverageButMarksPartial(t *testing.T) {
	// A partial run may be decided over the answers it has, but it must record
	// what is missing so it can never masquerade as complete (R2-13).
	raw := completeRaw(rawNoul("llm", 0.9))
	raw.Coverage = "partial"
	raw.Missing = []string{"form"}
	proposals, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatalf("partial coverage should decide: %v", err)
	}
	marked := false
	for _, incomplete := range proposals.Incomplete {
		if incomplete == "missing:form" {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("partial coverage must record its missing questions: %v", proposals.Incomplete)
	}
	// An unknown coverage value is still a contract error.
	raw.Coverage = "mystery"
	if _, err := Decide(raw, DefaultPolicy()); err == nil {
		t.Fatal("unknown coverage must not decide")
	}
}

// --- Margin has no independent power (SC09 / B04-T08) ----------------------

func TestChoiceMarginHasNoIndependentPowerOnNormalizedDistribution(t *testing.T) {
	// On a normalized distribution, best>=0.65 already implies best-second>=0.15
	// when second <= 0.35. Check the implication boundary both ways.
	accept, margin := DefaultPolicy().ChoiceAccept, DefaultPolicy().ChoiceMargin
	for best := 0.0; best <= 1.0; best += 0.01 {
		second := 1 - best
		passesAccept := best >= accept
		passesMargin := best-second >= margin
		if passesAccept && !passesMargin {
			t.Fatalf("accept threshold did not imply margin at best=%.2f", best)
		}
	}
	if !MarginHasNoIndependentPower(0.65, 0.35, accept, margin) {
		t.Fatal("documented boundary should hold")
	}
	if MarginHasNoIndependentPower(0.65, 0.35, 0.5, 0.15) {
		t.Fatal("non-boundary must not claim the implication")
	}
}

// --- Score distributions survive (SC09) ------------------------------------

func TestScoreRetainsDistributionAndDoesNotMultiplyConfidence(t *testing.T) {
	levels := []string{"low", "medium", "high"}
	confidence := 0.9
	first := rawScore("importance", 2, levels, map[string]float64{"low": 0.1, "medium": 0.2, "high": 0.7})
	first.Confidence = &confidence
	second := rawScore("importance", 2, levels, map[string]float64{"low": 0.3, "medium": 0.3, "high": 0.4})
	rawFirst := completeRaw(first, rawNoul("llm", 0.9))
	rawSecond := completeRaw(second, rawNoul("llm", 0.9))
	firstFeature := DescribeDistribution(first.Probabilities)
	secondFeature := DescribeDistribution(second.Probabilities)
	if math.Abs(firstFeature.MaxProb-secondFeature.MaxProb) < 1e-9 {
		t.Fatal("same mean must not collapse different distributions")
	}
	proposalsFirst, _ := Decide(rawFirst, DefaultPolicy())
	proposalsSecond, _ := Decide(rawSecond, DefaultPolicy())
	if proposalsFirst.Scores["importance"] != proposalsSecond.Scores["importance"] {
		t.Fatal("same ordinal score should map to the same bucket")
	}
	if math.Abs(firstFeature.Entropy-secondFeature.Entropy) < 1e-9 {
		t.Fatal("entropy must distinguish the distributions")
	}
}

// --- Display never deletes (SC13) ------------------------------------------

func TestDisplayFoldNeverDeletesEffectiveTopics(t *testing.T) {
	raw := completeRaw(
		rawNoul("llm", 0.95), rawNoul("eval", 0.94), rawNoul("eng", 0.93), rawNoul("science", 0.92),
		rawChoice("form", "method", map[string]float64{"method": 0.95, "none": 0.05}),
		rawChoice("use", "try", map[string]float64{"try": 0.95, "none": 0.05}),
	)
	policy := DefaultPolicy()
	proposals, err := Decide(raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals.Topics) != 4 {
		t.Fatalf("effective topics = %v, want 4", proposals.Topics)
	}
	shown, folded := PartitionTopics(proposals.Topics, policy)
	if len(shown) != 3 || len(folded) != 1 {
		t.Fatalf("shown/folded = %d/%d, want 3/1", len(shown), len(folded))
	}
	if len(append(append([]string{}, shown...), folded...)) != 4 {
		t.Fatal("folding lost a topic")
	}
}

// --- Replay is pure and zero-call (SC08) -----------------------------------

func TestReplayChangesDecisionWithoutNewEvidence(t *testing.T) {
	raw := completeRaw(
		rawNoul("llm", 0.7), rawNoul("eval", 0.3), rawNoul("eng", 0.1), rawNoul("science", 0.1),
		rawChoice("form", "method", map[string]float64{"method": 0.95, "none": 0.05}),
		rawChoice("use", "try", map[string]float64{"try": 0.95, "none": 0.05}),
	)
	oldPolicy := DefaultPolicy()
	newPolicy := DefaultPolicy()
	newPolicy.Version = "jev-policy-v2.1"
	newPolicy.TopicAccept = 0.6
	newPolicy.TopicReject = 0.4
	before, after, changed, err := Replay(raw, oldPolicy, newPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Topics) != 0 {
		t.Fatalf("old policy should not accept 0.7? got %v", before.Topics)
	}
	if len(after.Topics) != 1 || after.Topics[0] != "llm" {
		t.Fatalf("new threshold should accept llm: %v", after.Topics)
	}
	if len(changed) == 0 {
		t.Fatal("a changed decision must be reported")
	}
}

func TestReplayWithoutStoredRunFailsExplicitly(t *testing.T) {
	if _, _, _, err := Replay(RawJudgments{}, DefaultPolicy(), DefaultPolicy()); !errors.Is(err, ErrNoReplayableRun) {
		t.Fatalf("err = %v, want ErrNoReplayableRun", err)
	}
}

func TestCacheKeySeparatesModelAndSemantics(t *testing.T) {
	if CacheKey("e", "s", "m", "b") == CacheKey("e", "s", "m2", "b") {
		t.Fatal("different models must not share a cache key")
	}
	if CacheKey("e", "s", "m", "b") == CacheKey("e", "s2", "m", "b") {
		t.Fatal("different specs must not share a cache key")
	}
}
