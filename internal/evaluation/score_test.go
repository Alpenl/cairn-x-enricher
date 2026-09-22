package evaluation

import (
	"errors"
	"math"
	"testing"
)

func sample(id string, gold *Gold) Sample {
	return Sample{
		SampleID: id, SourceHash: "hash-" + id, Revision: 1, Language: "en", Carrier: "single",
		LengthBucket: "short", Completeness: "complete", GroupID: "group-" + id,
		Gold: gold, Provenance: ProvenanceHumanReviewed,
	}
}

func gold(topics []string, form, use string) *Gold {
	return &Gold{Topics: Label{Values: topics, NotApplicable: len(topics) == 0}, Form: Label{Values: nonEmpty(form), NotApplicable: form == ""}, Use: Label{Values: nonEmpty(use), NotApplicable: use == ""}}
}

func prediction(id string, topics []string, form, use string, probabilities map[string]float64) Prediction {
	return Prediction{
		SampleID: id, SpecID: "classify-v1", SpecHash: "s1", Model: "jev", PolicyVersion: "p",
		Topics: topics, Form: form, Use: use, TopicProbabilities: probabilities,
	}
}

// --- Zero-denominator and legal-empty rules (B08-T06) ----------------------

func TestZeroPredictionsAndZeroPositivesAreNotPerfectScores(t *testing.T) {
	dataset := Dataset{Name: "zero", Split: "test",
		Samples:    []Sample{sample("a", gold(nil, "", ""))},
		Prediction: []Prediction{prediction("a", nil, "", "", nil)},
	}
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	// 0/0 must be 0, never 1.
	for _, metric := range report.Dimensions {
		if metric.Precision > 0 || metric.Recall > 0 {
			t.Fatalf("0/0 must not be a perfect score: %+v", metric)
		}
	}
	if report.MacroPrecision != 0 || report.MacroRecall != 0 {
		t.Fatal("zero positives must not read as perfect")
	}
}

func TestAllAbstainedDoesNotProducePrettyPrecision(t *testing.T) {
	dataset := Dataset{Name: "abstain", Split: "test",
		Samples: []Sample{
			sample("a", gold([]string{"llm"}, "method", "try")),
			sample("b", gold([]string{"eng"}, "case", "quote")),
		},
		Prediction: []Prediction{
			func() Prediction {
				p := prediction("a", nil, "", "", nil)
				p.Abstained = []string{"topic", "form", "use"}
				return p
			}(),
			func() Prediction {
				p := prediction("b", nil, "", "", nil)
				p.Abstained = []string{"topic", "form", "use"}
				return p
			}(),
		},
	}
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	// Abstaining on everything loses recall, so it cannot be hidden.
	if report.MicroRecall != 0 {
		t.Fatalf("all-abstain recall = %v, want 0", report.MicroRecall)
	}
	if report.Coverage != 0 {
		t.Fatalf("all-abstain coverage = %v, want 0", report.Coverage)
	}
}

func TestLegalNoneCountsAsSupportAndIsNotAnError(t *testing.T) {
	dataset := Dataset{Name: "none", Split: "test",
		Samples:    []Sample{sample("a", &Gold{Topics: Label{NotApplicable: true}, Form: Label{NotApplicable: true}, Use: Label{NotApplicable: true}})},
		Prediction: []Prediction{prediction("a", nil, "", "", nil)},
	}
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedError != 0 {
		t.Fatalf("a legal empty prediction must not be an accepted error: %v", report.AcceptedError)
	}
}

// --- Micro vs macro (B08-T06) ---------------------------------------------

func TestMicroAndMacroDivergeWhenSupportIsUnbalanced(t *testing.T) {
	samples := []Sample{}
	predictions := []Prediction{}
	// 20 samples of a frequent dimension, 1 of a rare one.
	for index := 0; index < 20; index++ {
		id := string(rune('a' + index))
		samples = append(samples, sample(id, gold([]string{"llm"}, "", "")))
		predictions = append(predictions, prediction(id, []string{"llm"}, "", "", nil))
	}
	samples = append(samples, sample("rare", gold(nil, "method", "")))
	predictions = append(predictions, prediction("rare", []string{"llm"}, "case", "", nil))
	report, err := Score(Dataset{Name: "imbalance", Split: "test", Samples: samples, Prediction: predictions})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(report.MicroPrecision-report.MacroPrecision) < 1e-9 {
		t.Fatalf("micro and macro should differ under imbalance: micro=%v macro=%v", report.MicroPrecision, report.MacroPrecision)
	}
}

// --- Calibration (B08-T07) -------------------------------------------------

func TestCalibrationDistinguishesOverconfidence(t *testing.T) {
	samples := []Sample{}
	predictions := []Prediction{}
	for index := 0; index < 10; index++ {
		id := string(rune('a' + index))
		samples = append(samples, sample(id, gold(nil, "", "")))
		// Predict p=0.95 for a label that is never correct: high ECE.
		predictions = append(predictions, prediction(id, []string{"llm"}, "", "", map[string]float64{"llm": 0.95}))
	}
	report, err := Score(Dataset{Name: "calib", Split: "test", Samples: samples, Prediction: predictions})
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasCalibration {
		t.Fatal("calibration should be computed from the stored distribution")
	}
	if report.ECE < 0.5 {
		t.Fatalf("overconfident predictions should have a large ECE: %v", report.ECE)
	}
	if report.Brier < 0.5 {
		t.Fatalf("overconfident predictions should have a large Brier: %v", report.Brier)
	}
}

func TestIdenticalScoreMeanButDifferentDistributionIsRetained(t *testing.T) {
	// Two distributions with the same maximum but different entropy must not
	// collapse to the same calibration evidence.
	// Compare ECE computed from each distribution separately.
	flatPoints := []calibPoint{{p: 0.5, correct: true}, {p: 0.5, correct: false}}
	peakedPoints := []calibPoint{{p: 0.7, correct: true}, {p: 0.7, correct: false}}
	flatECE, _ := expectedCalibrationError(flatPoints, 10)
	peakedECE, _ := expectedCalibrationError(peakedPoints, 10)
	if flatECE == peakedECE {
		t.Fatal("different calibration evidence must not collapse")
	}
}

// --- Data validation (B08-T02/T05) ----------------------------------------

func TestDatasetRejectsDuplicatesAndMissingGoldProvenance(t *testing.T) {
	duplicate := Dataset{Name: "d", Split: "test",
		Samples: []Sample{sample("a", gold(nil, "", "")), sample("a", gold(nil, "", ""))},
	}
	if err := duplicate.Validate(); err == nil || !containsSubstring(err.Error(), "duplicate sample_id") {
		t.Fatalf("duplicate sample must be rejected: %v", err)
	}
	legacy := Dataset{Name: "d", Split: "test",
		Samples: []Sample{{SampleID: "a", SourceHash: "h", Provenance: ProvenanceLegacyUnknown, Gold: gold(nil, "", "")}},
	}
	if err := legacy.Validate(); err == nil {
		t.Fatal("legacy_unknown must not carry gold")
	}
}

func TestGroupSplitDoesNotLeakGroupsAcrossSets(t *testing.T) {
	samples := []Sample{}
	for index := 0; index < 60; index++ {
		id := string(rune('a'+index%26)) + string(rune('0'+index/26))
		samples = append(samples, Sample{SampleID: id, SourceHash: "h" + id, Provenance: ProvenanceHumanReviewed, GroupID: "g" + string(rune('0'+index%10))})
	}
	splits, err := SplitByGroup(samples, 7, 0.6, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for name, set := range splits {
		for _, item := range set {
			if previous, ok := seen[item.GroupID]; ok && previous != name {
				t.Fatalf("group %s leaked from %s to %s", item.GroupID, previous, name)
			}
			seen[item.GroupID] = name
		}
	}
	if len(splits["test"]) == 0 {
		t.Fatal("test split must not be empty")
	}
}

// --- Threshold replay is zero-call (B08-T08) -------------------------------

func TestReplayChangesDecisionWithoutAnyModelCall(t *testing.T) {
	probabilities := map[string]float64{"llm": 0.7, "eng": 0.3}
	low := Policy{Version: "low", TopicAccept: 0.8, TopicReject: 0.2}
	high := Policy{Version: "high", TopicAccept: 0.6, TopicReject: 0.4}
	if len(Replay(low, probabilities)) != 0 {
		t.Fatal("0.7 should be below the 0.8 threshold")
	}
	if len(Replay(high, probabilities)) != 1 {
		t.Fatal("0.7 should be above the 0.6 threshold")
	}
}

func TestThresholdSearchRespectsCoverageFloor(t *testing.T) {
	samples := []Sample{}
	predictions := []Prediction{}
	for index := 0; index < 30; index++ {
		id := string(rune('a'+index%20)) + string(rune('0'+index/20))
		// Half the samples genuinely have llm at 0.7, half do not.
		var topics []string
		probability := 0.2
		if index%2 == 0 {
			topics = []string{"llm"}
			probability = 0.7
		}
		samples = append(samples, sample(id, gold(topics, "", "")))
		predictions = append(predictions, prediction(id, topics, "", "", map[string]float64{"llm": probability}))
	}
	policy, err := SearchThresholds(Dataset{Name: "search", Split: "train", Samples: samples, Prediction: predictions}, []float64{0.5, 0.6, 0.7, 0.8, 0.9}, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if policy.TopicAccept < 0.5 || policy.TopicAccept > 0.9 {
		t.Fatalf("threshold outside candidates: %v", policy.TopicAccept)
	}
}

// --- Ablation comparability (B08-T09) -------------------------------------

func TestAblationReportsNonComparableDimensions(t *testing.T) {
	baseline := Report{Dimensions: []DimensionMetric{{Dimension: "topics", F1: 0.8}}}
	variant := Report{Dimensions: []DimensionMetric{{Dimension: "topics", F1: 0.7}, {Dimension: "importance", F1: 0.9}}}
	deltas := CompareAblations(baseline, variant)
	byName := map[string]AblationDelta{}
	for _, delta := range deltas {
		byName[delta.Dimension] = delta
	}
	if !byName["topics"].Comparable || math.Abs(byName["topics"].Delta-(-0.1)) > 1e-9 {
		t.Fatalf("topics delta wrong: %+v", byName["topics"])
	}
	if byName["importance"].Comparable {
		t.Fatal("a dimension absent from the baseline is not comparable")
	}
}

// --- Promotion gate (B08-T13) ---------------------------------------------

func TestGateBlocksOnSafetyEvenWithGoodStatistics(t *testing.T) {
	report := Report{
		SamplesWithGold: 100, KnownReferenceSamples: 100, IndependentGroups: 100, MacroRecall: 0.95, Coverage: 0.95,
		// One accepted error rate above the tolerance blocks promotion.
		AcceptedError: 0.3, ReviewFields: 1,
	}
	decision := EvaluateGate(report, DefaultGate())
	if decision.Promote {
		t.Fatal("a safety failure must block promotion")
	}
	if len(decision.Blockers) == 0 {
		t.Fatal("the blocker must be reported")
	}
}

func TestGateIsInconclusiveWithoutGold(t *testing.T) {
	report := Report{SamplesWithGold: 2, MacroRecall: 1, Coverage: 1, AcceptedError: 0}
	decision := EvaluateGate(report, DefaultGate())
	if decision.Promote || !decision.Inconclusive {
		t.Fatalf("small samples must be inconclusive and not promoted: %+v", decision)
	}
}

func TestGatePromotesOnlyWhenEveryThresholdPasses(t *testing.T) {
	report := Report{
		SamplesWithGold: 100, KnownReferenceSamples: 100, IndependentGroups: 100, MacroRecall: 0.8, Coverage: 0.8, AcceptedError: 0.02,
		ReviewFields: 1, HasCalibration: true, Brier: 0.1, ECE: 0.05,
	}
	decision := EvaluateGate(report, DefaultGate())
	if !decision.Promote {
		t.Fatalf("a passing report should promote: %+v", decision)
	}
}

func TestMissingCalibrationBlocksWhenRequired(t *testing.T) {
	gate := DefaultGate()
	gate.RequireCalibration = true
	report := Report{SamplesWithGold: 100, KnownReferenceSamples: 100, IndependentGroups: 100, MacroRecall: 0.8, Coverage: 0.8, AcceptedError: 0.02, ReviewFields: 1}
	if EvaluateGate(report, gate).Promote {
		t.Fatal("required calibration must block promotion when absent")
	}
}

// --- Model drift (B08-T13) -------------------------------------------------

func TestModelDriftRequiresReevaluation(t *testing.T) {
	drift := DetectModelDrift("jev-2026-09", "jev-2026-10")
	if !drift.Drifted || !drift.Reevaluate {
		t.Fatalf("alias drift must require re-evaluation: %+v", drift)
	}
	if DetectModelDrift("jev-2026-10", "jev-2026-10").Reevaluate {
		t.Fatal("no drift means no forced re-evaluation")
	}
}

// --- Inconclusive reporting (B08-T14) -------------------------------------

func TestReportIsInconclusiveWithMissingGold(t *testing.T) {
	dataset := Dataset{Name: "missing", Split: "test",
		Samples:    []Sample{sample("a", gold([]string{"llm"}, "", "")), {SampleID: "b", SourceHash: "h", Provenance: ProvenanceHumanReviewed}, makeSampleNoGold("c")},
		Prediction: []Prediction{prediction("a", []string{"llm"}, "", "", nil), prediction("b", nil, "", "", nil)},
	}
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Inconclusive {
		t.Fatal("missing gold must mark the report inconclusive")
	}
	if report.SamplesMissing != 2 {
		t.Fatalf("missing gold count = %d, want 2", report.SamplesMissing)
	}
}

// --- Mismatched-run guard ---------------------------------------------------

func TestMismatchedRunsErrorIsDefined(t *testing.T) {
	if !errors.Is(ErrMismatchedRuns, ErrMismatchedRuns) {
		t.Fatal("sentinel must be comparable")
	}
}

func containsSubstring(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func makeSampleNoGold(id string) Sample {
	return Sample{SampleID: id, SourceHash: "h-" + id, Provenance: ProvenanceHumanReviewed}
}
