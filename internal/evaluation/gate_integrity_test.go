package evaluation

import (
	"fmt"
	"math"
	"testing"
)

func passingScoredReport(t *testing.T) Report {
	t.Helper()
	d := Dataset{Name: "gate-engineering-fixture", Split: "holdout"}
	for i := range 30 {
		id := fmt.Sprint(i)
		s := sample(id, gold([]string{"llm"}, "method", "try"))
		s.Provenance = ProvenanceSynthetic
		d.Samples = append(d.Samples, s)
		d.Prediction = append(d.Prediction, prediction(id, []string{"llm"}, "method", "try", map[string]float64{"llm": 0.9}))
	}
	r, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGateRejectsInvalidFrozenThresholds(t *testing.T) {
	r := passingScoredReport(t)
	for name, mutate := range map[string]func(*GateThresholds){
		"nan error limit":       func(g *GateThresholds) { g.MaxAcceptedError = math.NaN() },
		"infinite review limit": func(g *GateThresholds) { g.MaxReviewFields = math.Inf(1) },
		"negative sample floor": func(g *GateThresholds) { g.MinGoldSamples = -1 },
		"zero group floor":      func(g *GateThresholds) { g.MinIndependentGroups = 0 },
		"impossible coverage":   func(g *GateThresholds) { g.MinCoverage = -0.1 },
		"impossible Brier":      func(g *GateThresholds) { g.MaxBrier = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			g := DefaultGate()
			mutate(&g)
			if got := EvaluateGate(r, g); got.Promote || len(got.Blockers) == 0 {
				t.Fatalf("invalid gate accepted: %+v", got)
			}
		})
	}
}

func TestGateRejectsInvalidReportAndNonHoldout(t *testing.T) {
	for name, mutate := range map[string]func(*Report){
		"coverage above one":     func(r *Report) { r.Coverage = 2 },
		"recall above one":       func(r *Report) { r.MacroRecall = 2 },
		"inconsistent counts":    func(r *Report) { r.DecidedFields = r.KnownFields + 1 },
		"no reference identity":  func(r *Report) { r.ReferenceHash = "" },
		"training split":         func(r *Report) { r.Split = "train" },
		"development split":      func(r *Report) { r.Split = "dev" },
		"negative missing count": func(r *Report) { r.SamplesMissing = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := passingScoredReport(t)
			mutate(&r)
			if got := EvaluateGate(r, DefaultGate()); got.Promote || len(got.Blockers) == 0 {
				t.Fatalf("invalid report accepted: %+v", got)
			}
		})
	}
}

func TestAblationRequiresSameReferenceAndSupportedTask(t *testing.T) {
	for name, mutate := range map[string]func(*Report){
		"different references": func(r *Report) { r.ReferenceHash = "another-reference" },
		"unknown references":   func(r *Report) { r.ReferenceHash = "" },
		"different split":      func(r *Report) { r.Split = "dev" },
		"different task type":  func(r *Report) { r.Dimensions[0].MultiLabel = false },
		"no support":           func(r *Report) { r.Dimensions[0].Support = 0 },
		"invalid metric":       func(r *Report) { r.Dimensions[0].F1 = 2 },
		"duplicate dimension":  func(r *Report) { r.Dimensions = append(r.Dimensions, r.Dimensions[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			baseline, variant := passingScoredReport(t), passingScoredReport(t)
			mutate(&variant)
			for _, delta := range CompareAblations(baseline, variant) {
				if delta.Dimension == "topics" && (delta.Comparable || delta.Delta != 0) {
					t.Fatalf("invalid comparison accepted: %+v", delta)
				}
			}
		})
	}
}

func TestAblationRetainsBaselineOnlyDimension(t *testing.T) {
	baseline, variant := passingScoredReport(t), passingScoredReport(t)
	variant.Dimensions = variant.Dimensions[1:]
	for _, delta := range CompareAblations(baseline, variant) {
		if delta.Dimension == "topics" {
			if delta.Comparable || delta.Delta != 0 {
				t.Fatalf("missing dimension compared: %+v", delta)
			}
			return
		}
	}
	t.Fatal("baseline-only dimension disappeared")
}

func TestGateZeroBrierLimitIsEnforced(t *testing.T) {
	g := DefaultGate()
	g.MaxBrier = 0
	if got := EvaluateGate(passingScoredReport(t), g); got.Promote {
		t.Fatal("zero Brier limit silently disabled")
	}
}
