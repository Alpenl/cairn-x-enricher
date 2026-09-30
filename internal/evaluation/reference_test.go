package evaluation

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func metricFor(t *testing.T, r Report, name string) DimensionMetric {
	t.Helper()
	for _, m := range r.Dimensions {
		if m.Dimension == name {
			return m
		}
	}
	t.Fatalf("missing %s", name)
	return DimensionMetric{}
}
func TestAcceptedErrorsIncludeEveryDimension(t *testing.T) {
	labels := Label{Values: []string{"correct"}}
	g := &Gold{Topics: labels, ContentFunctions: labels, Carriers: labels, Affordances: labels, Form: labels, Use: labels}
	p := Prediction{SampleID: "a", Topics: []string{"wrong"}, ContentFunctions: []string{"wrong"}, Carriers: []string{"wrong"}, Affordances: []string{"wrong"}, Form: "wrong", Use: "wrong"}
	r, err := Score(Dataset{Name: "wrong", Samples: []Sample{sample("a", g)}, Prediction: []Prediction{p}})
	if err != nil {
		t.Fatal(err)
	}
	if r.AcceptedError != 1 || r.MicroRecall != 0 || r.KnownFields != 6 || r.Coverage != 1 {
		t.Fatalf("wrong accepted values were hidden: %+v", r)
	}
	for _, m := range r.Dimensions {
		if m.FalsePos != 1 || m.FalseNeg != 1 {
			t.Fatalf("wrong dimension accounting: %+v", m)
		}
	}
}
func TestUnknownIsExcludedAndAcceptableChoiceIsOneOutcome(t *testing.T) {
	g := &Gold{Topics: Label{Unknown: true}, Form: Label{Values: []string{"method", "case"}}, Carriers: Label{Values: []string{"single", "thread"}}, Use: Label{NotApplicable: true}}
	p := prediction("a", []string{"llm"}, "case", "", map[string]float64{"llm": 0.99})
	p.Carriers = []string{"thread"}
	r, err := Score(Dataset{Name: "alternatives", Samples: []Sample{sample("a", g)}, Prediction: []Prediction{p}})
	if err != nil {
		t.Fatal(err)
	}
	if r.HasCalibration || r.AcceptedError != 0 || r.KnownFields != 3 || r.Coverage != 1 {
		t.Fatalf("unknown or alternatives scored incorrectly: %+v", r)
	}
	if m := metricFor(t, r, "topics"); m.Support != 0 || m.Unknown != 1 || m.FalsePos != 0 {
		t.Fatal(m)
	}
	for _, name := range []string{"form", "carriers"} {
		if m := metricFor(t, r, name); m.TruePositive != 1 || m.FalseNeg != 0 {
			t.Fatal(m)
		}
	}
	if m := metricFor(t, r, "use"); m.CorrectEmpty != 1 {
		t.Fatal(m)
	}
}
func TestMissingPredictionsCannotPassEvenWithThirtyReferences(t *testing.T) {
	d := Dataset{Name: "missing", Split: "holdout"}
	for i := range 30 {
		id := fmt.Sprint(i)
		d.Samples = append(d.Samples, sample(id, gold([]string{"llm"}, "method", "try")))
	}
	r, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Inconclusive || r.Coverage != 0 || r.MissingGoldCount != 30 || r.ReviewFields != 3 {
		t.Fatalf("missing predictions: %+v", r)
	}
	if m := metricFor(t, r, "form"); m.Missing != 30 || m.Abstained != 0 || m.FalseNeg != 30 {
		t.Fatal(m)
	}
	if EvaluateGate(r, DefaultGate()).Promote {
		t.Fatal("missing predictions promoted")
	}
	// A previously good report still cannot promote if it carries inconclusive evidence.
	r = Report{SamplesWithGold: 100, KnownReferenceSamples: 100, IndependentGroups: 100, MacroRecall: 1, Coverage: 1, Inconclusive: true, InconclusiveWhy: []string{"missing prediction"}}
	if EvaluateGate(r, DefaultGate()).Promote {
		t.Fatal("gate ignored report status")
	}
}
func TestNoReferencesProducesFiniteJSON(t *testing.T) {
	d := Dataset{Name: "none", Samples: []Sample{makeSampleNoGold("a")}, Prediction: []Prediction{prediction("a", nil, "", "", nil)}}
	r, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatalf("nonfinite report: %v", err)
	}
	if !r.Inconclusive || r.Coverage != 0 {
		t.Fatal(r)
	}
}
func TestReferenceHashIsFrozenBeforePredictions(t *testing.T) {
	d := Dataset{Name: "frozen", Samples: []Sample{sample("a", gold([]string{"llm"}, "", ""))}}
	before, err := HashReference(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Prediction = []Prediction{prediction("a", []string{"wrong"}, "", "", nil)}
	after, err := HashReference(d)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("reference identity depends on candidate output")
	}
}
func TestSplitValidatorRejectsHiddenLeakage(t *testing.T) {
	for _, kind := range []string{"sample", "source", "group"} {
		t.Run(kind, func(t *testing.T) {
			a, b := sample("a", nil), sample("b", nil)
			switch kind {
			case "sample":
				b.SampleID = a.SampleID
			case "source":
				b.SourceHash = a.SourceHash
			case "group":
				b.GroupID = a.GroupID
			}
			if err := ValidateSplits([]Dataset{{Name: "train", Split: "train", Samples: []Sample{a}}, {Name: "test", Split: "holdout", Samples: []Sample{b}}}); err == nil {
				t.Fatal("leak accepted")
			}
		})
	}
}
func TestReferenceProvenanceAndProbabilityValidation(t *testing.T) {
	d := Dataset{Name: "references", Samples: []Sample{sample("a", gold(nil, "", ""))}}
	d.Samples[0].Provenance = ProvenanceAutomaticReference
	if err := d.Validate(); err == nil {
		t.Fatal("unattributed automatic reference accepted")
	}
	d.Samples[0].Reference = &ReferenceMetadata{Method: "constructed_scenario", Version: "v1", Basis: "fixed before inference", Source: "synthetic"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Samples[0].Provenance = "made-up"
	if err := d.Validate(); err == nil {
		t.Fatal("unknown provenance accepted")
	}
	d.Samples[0].Provenance = ProvenanceSynthetic
	for _, value := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		d.Prediction = []Prediction{prediction("a", nil, "", "", map[string]float64{"llm": value})}
		if err := d.Validate(); err == nil {
			t.Fatal("invalid probability accepted")
		}
	}
}
func TestHoldoutCannotFitThresholds(t *testing.T) {
	d := Dataset{Name: "holdout", Split: "holdout", Samples: []Sample{sample("a", gold(nil, "", ""))}}
	if _, err := SearchThresholds(d, []float64{0.5}, 0); err == nil {
		t.Fatal("holdout used for fitting")
	}
	d.Split = "train"
	if _, err := SearchThresholds(d, []float64{math.NaN()}, 0); err == nil {
		t.Fatal("invalid threshold accepted")
	}
}
func TestBootstrapResamplesGroupsAndIsDeterministic(t *testing.T) {
	d := Dataset{Name: "groups"}
	for i := range 12 {
		id := fmt.Sprint(i)
		item := sample(id, gold([]string{"llm"}, "", ""))
		item.GroupID = fmt.Sprint(i % 2)
		d.Samples = append(d.Samples, item)
		d.Prediction = append(d.Prediction, prediction(id, []string{"llm"}, "", "", nil))
	}
	r, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	ci := r.Intervals["micro_precision"]
	if ci.Groups != 2 || ci.Replicates != 500 || ci.Low != 1 || ci.High != 1 {
		t.Fatal(ci)
	}
	again, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	if again.Intervals["micro_precision"] != ci {
		t.Fatal("nondeterministic bootstrap")
	}
}
