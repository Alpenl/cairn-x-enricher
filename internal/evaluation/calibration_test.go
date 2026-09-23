package evaluation

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func calibrationConfig() CalibrationConfig {
	return CalibrationConfig{Version: "engineering-diagnostics-v1", Bins: 10, Policy: classify.DefaultPolicy(), Cutoffs: []float64{0, .3, .65, .9, 1}}
}

func probabilityMetric(t *testing.T, report CalibrationArtifact, dimension string) ProbabilityMetric {
	t.Helper()
	for _, m := range report.Dimensions {
		if m.Name == dimension {
			return m
		}
	}
	t.Fatalf("missing metric %s", dimension)
	return ProbabilityMetric{}
}

func TestCalibrationUsesVerifiedFullRawAndProductionDecisions(t *testing.T) {
	d, calls := policyDataset(t)
	before := calls.Load()
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before || r.ModelCalls != 0 || r.Promote || r.Calibrated || r.OrdinalQualityEvaluated || r.ScoreObservations != 0 {
		t.Fatal("calibration fabricated inference or quality")
	}
	topic, choice := probabilityMetric(t, r, "topics"), probabilityMetric(t, r, "form")
	if math.Abs(*topic.Brier-.03625) > 1e-12 || math.Abs(*choice.Brier-.18) > 1e-12 || math.Abs(*choice.ECE-.3) > 1e-12 {
		t.Fatalf("wrong scoring rule: %+v %+v", topic, choice)
	}
	if topic.Observations != 4 || topic.IndependentGroups != 2 || !topic.Inconclusive || choice.Bins[7].Count != 2 || choice.Bins[7].ObservedRate == nil || *choice.Bins[7].ObservedRate != 1 || choice.Bins[0].MeanProbability != nil {
		t.Fatal("lost observation/group/bin semantics")
	}
	for _, p := range r.ChoiceRisk {
		if p.Dimension != "all_choices" {
			continue
		}
		if p.Feature == "production_policy" && (p.Support != 6 || p.Accepted != 6 || p.AcceptedError == nil || *p.AcceptedError != 0) {
			t.Fatalf("wrong production baseline: %+v", p)
		}
		if p.Feature == "confidence" && (p.MissingFeature != 6 || p.Accepted != 0 || p.AcceptedError != nil) {
			t.Fatal("missing confidence invented as zero-risk evidence")
		}
	}
	again, err := v.Calibration(calibrationConfig())
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("nondeterministic calibration")
	}
}

func TestCalibrationUnknownAmbiguousAndMissingCandidates(t *testing.T) {
	d, _ := policyDataset(t)
	for i := range d.Samples {
		d.Samples[i].Gold.Topics = Label{Unknown: true}
		d.Samples[i].Gold.Form = Label{Values: []string{"method", "none"}}
	}
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	topic, form := probabilityMetric(t, r, "topics"), probabilityMetric(t, r, "form")
	if topic.Brier != nil || topic.ECE != nil || topic.UnknownReference != 4 || form.AmbiguousReference != 2 || form.Brier != nil {
		t.Fatal("invented targets for unknown or alternative references")
	}
	d.Samples[0].Gold.Topics = Label{Values: []string{"absent_from_catalog"}}
	v, err = PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.Calibration(calibrationConfig()); err == nil {
		t.Fatal("missing positive candidate became a false-negative-free calibration")
	}
}

func TestCalibrationPreservesNoneProductionAcceptanceAndData(t *testing.T) {
	d, _ := policyDataset(t)
	for i := range d.Prediction {
		j := d.Prediction[i].Evaluation.Judgments["form"]
		j.Choice = "none"
		d.Prediction[i].Evaluation.Judgments["form"] = j
	}
	before, _ := json.Marshal(d)
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.ChoiceRisk {
		if p.Dimension != "form" {
			continue
		}
		if p.Feature == "production_policy" && (p.Accepted != 2 || p.Errors != 2) {
			t.Fatal("replaced production none acceptance with a made-up threshold")
		}
		if p.Feature == "chosen_probability" && p.Cutoff == .65 && (p.Accepted != 0 || p.AcceptedError != nil) {
			t.Fatal("empty selection masquerades as zero error")
		}
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("mutated references or probabilities")
	}
	r.Config.Cutoffs[0] = .123
	again, err := v.Calibration(calibrationConfig())
	if err != nil || again.Config.Cutoffs[0] != 0 {
		t.Fatal("exposed mutable config")
	}
}

func TestCalibrationRejectsHoldoutAndInvalidGrids(t *testing.T) {
	d, _ := policyDataset(t)
	d.Split = "holdout"
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Calibration(calibrationConfig()); err == nil {
		t.Fatal("exploratory diagnostics used holdout")
	}
	for _, bad := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
		c := calibrationConfig()
		c.Cutoffs = []float64{bad}
		if c.Validate() == nil {
			t.Fatalf("accepted cutoff %v", bad)
		}
	}
	for _, bins := range []int{-1, 0, 1, 51} {
		c := calibrationConfig()
		c.Bins = bins
		if c.Validate() == nil {
			t.Fatal("invalid bins")
		}
	}
	c := calibrationConfig()
	c.Cutoffs = []float64{.5, .5}
	if c.Validate() == nil {
		t.Fatal("duplicate cutoffs")
	}
}

func TestReliabilityBinsIncludeEndpointsAndRetainEmptyBins(t *testing.T) {
	a := &probabilityAccumulator{groups: map[string]bool{"group": true}, points: []calibPoint{{p: 0, correct: false}, {p: .1, correct: true}, {p: 1, correct: true}}, brier: .81}
	m := finishProbability(a, 10)
	if m.Bins[0].Count != 1 || m.Bins[1].Count != 1 || m.Bins[9].Count != 1 || m.Bins[2].ObservedRate != nil || math.Abs(*m.Brier-.27) > 1e-12 || math.Abs(*m.ECE-.3) > 1e-12 {
		t.Fatalf("bad endpoint bins: %+v", m)
	}
}

func ordinalFixture(t *testing.T, probabilities map[string]float64, mean float64) (classify.Question, classify.RawAnswer) {
	t.Helper()
	criteria, _ := json.Marshal([]string{"no actionable steps", "some required steps missing", "complete reproducible procedure"})
	q := classify.Question{ID: "completeness", Kind: classify.QuestionScore, Dimension: "completeness", Instructions: json.RawMessage(`"Rate procedural completeness"`), Criteria: criteria}
	return q, classify.RawAnswer{Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: mean, Legend: q.ScoreLegend(), Probabilities: probabilities}}
}

func TestIdenticalScoreMeanRetainsDifferentOrdinalDistribution(t *testing.T) {
	q, peaked := ordinalFixture(t, map[string]float64{"0": 0, "1": 1, "2": 0}, 1)
	_, wide := ordinalFixture(t, map[string]float64{"0": .5, "1": 0, "2": .5}, 1)
	a, err := ScoreOrdinal(q, peaked, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ScoreOrdinal(q, wide, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.AbsoluteError != 0 || b.AbsoluteError != 0 || a.RankedProbabilityScore != 0 || b.RankedProbabilityScore != .25 || a.ExpectedAbsoluteError != 0 || b.ExpectedAbsoluteError != 1 || reflect.DeepEqual(a.Probabilities, b.Probabilities) {
		t.Fatalf("same mean lost ordinal risk: %+v %+v", a, b)
	}
	b.Probabilities[0] = 0
	if wide.Score.Probabilities["0"] != .5 {
		t.Fatal("metric mutated raw distribution")
	}
}

func TestOrdinalDistanceAndInvalidContracts(t *testing.T) {
	q, answer := ordinalFixture(t, map[string]float64{"0": 0, "1": 0, "2": 1}, 2)
	near, err := ScoreOrdinal(q, answer, 1)
	if err != nil {
		t.Fatal(err)
	}
	far, err := ScoreOrdinal(q, answer, 0)
	if err != nil {
		t.Fatal(err)
	}
	if near.RankedProbabilityScore != .5 || far.RankedProbabilityScore != 1 || near.AbsoluteError != 1 || far.AbsoluteError != 2 {
		t.Fatal("ordinal distance was treated as nominal mismatch")
	}
	for _, expected := range []int{-1, 3} {
		if _, err := ScoreOrdinal(q, answer, expected); err == nil {
			t.Fatal("accepted out-of-range reference")
		}
	}
	answer.Score.Legend["0"] = "different rubric"
	if _, err := ScoreOrdinal(q, answer, 1); err == nil {
		t.Fatal("accepted changed level meaning")
	}
}
