package evaluation

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func policyDataset(t *testing.T) (Dataset, *atomic.Int64) {
	t.Helper()
	spec, err := classify.CompileSpec(exportCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]classify.RawAnswer{}
	for _, q := range spec.Questions {
		if q.Kind == classify.QuestionNoul {
			p := 0.75
			if q.TermID == "eval" {
				p = 0.1
			}
			answers[q.ID] = classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &p}}
		} else {
			option := "method"
			if q.Dimension == "use" {
				option = "try"
			}
			if q.Dimension == "carriers" {
				option = "single"
			}
			distribution := map[string]float64{}
			for _, id := range q.AnswerOptions() {
				distribution[id] = 0
			}
			distribution[option] = 0.7
			distribution["none"] = 0.3
			answers[q.ID] = classify.RawAnswer{Type: classify.TypeChoice, Choice: &classify.ChoiceAnswer{Choice: option, Probabilities: distribution}}
		}
	}
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}})
	}))
	t.Cleanup(server.Close)
	client, err := classify.NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), exportCatalog())
	if err != nil {
		t.Fatal(err)
	}
	d := liveDataset(t)
	for i, s := range d.Samples {
		d.Samples[i].Gold = &Gold{Topics: Label{Values: []string{"llm"}}, ContentFunctions: Label{Values: []string{"method"}}, Carriers: Label{Values: []string{"single"}}, Affordances: Label{Values: []string{"practice"}}, Form: Label{Values: []string{"method"}}, Use: Label{Values: []string{"try"}}}
		raw, err := client.Evaluate(context.Background(), classify.Input{Evidence: s.Material})
		if err != nil {
			t.Fatal(err)
		}
		p, err := PredictionFromJudgments(s.SampleID, raw, client.Policy())
		if err != nil {
			t.Fatal(err)
		}
		p.Evaluation = &raw
		d.Prediction = append(d.Prediction, p)
	}
	return d, calls
}

func fitConfig() PolicyFitConfig {
	weights, floors := map[string]float64{}, map[string]float64{}
	for _, d := range policyDimensions {
		weights[d] = 1
		floors[d] = 0.5
	}
	return PolicyFitConfig{Version: "engineering-risk-v1", Scope: "synthetic fixtures only", Base: classify.DefaultPolicy(), Accept: []float64{0.7, 0.8}, Reject: []float64{0.2}, Choice: []float64{0.65, 0.8}, MinCoverage: floors, MaxAcceptedError: 0.1, Risk: FitRisk{FalsePositive: 5, FalseNegative: 1, Review: 0.25, DimensionWeights: weights}}
}

func TestProductionPolicyFitUsesSixDimensionsAndZeroAdditionalCalls(t *testing.T) {
	d, calls := policyDataset(t)
	before := calls.Load()
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	fitted, err := FitProductionPolicy(v, fitConfig())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before || fitted.ModelCalls != 0 || fitted.Promote || fitted.Selected == nil || fitted.Selected.Calibrated {
		t.Fatalf("unsafe fit: %+v", fitted)
	}
	if len(fitted.Candidates) != 4 || fitted.Selected.TopicAccept != 0.7 || fitted.Selected.ChoiceAccept != 0.65 || fitted.Status != "fitted_unvalidated" {
		t.Fatalf("wrong selection: %+v", fitted.Selected)
	}
	if fitted.SelectedReport.IndependentGroups != 2 || !fitted.SelectedReport.Inconclusive {
		t.Fatal("training support became quality validation")
	}
	result, err := ReplayFittedPolicy(v, fitted)
	if err != nil {
		t.Fatal(err)
	}
	p := result.Prediction[0]
	if len(p.Topics) != 1 || len(p.ContentFunctions) != 1 || len(p.Affordances) != 1 || len(p.Carriers) != 1 || p.Form != "method" || p.Use != "try" || len(p.Abstained) != 0 {
		t.Fatalf("not a six-dimensional production replay: %+v", p)
	}
	ref, _ := HashReference(d)
	after, _ := HashReference(result)
	if ref != after || fitted.ReferenceHash != ref {
		t.Fatal("reference labels changed")
	}
	for _, m := range fitted.SelectedReport.Dimensions {
		if m.F1 != 1 {
			t.Fatalf("dimension missing: %+v", m)
		}
	}
	second, err := FitProductionPolicy(v, fitConfig())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fitted)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("fit is not reproducible")
	}
}

func TestFittedPolicyBindingRejectsTampering(t *testing.T) {
	d, _ := policyDataset(t)
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	a, err := FitProductionPolicy(v, fitConfig())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(a)
	for name, mutate := range map[string]func(*PolicyFitArtifact){
		"model":       func(a *PolicyFitArtifact) { a.Binding.Model = "jev-2.0.0" },
		"spec":        func(a *PolicyFitArtifact) { a.Binding.SpecHash = "changed" },
		"risk":        func(a *PolicyFitArtifact) { a.Config.Risk.FalsePositive = 10 },
		"threshold":   func(a *PolicyFitArtifact) { a.Selected.TopicAccept = 0.8 },
		"calibrated":  func(a *PolicyFitArtifact) { a.Selected.Calibrated = true },
		"holdout fit": func(a *PolicyFitArtifact) { a.Split = "holdout" },
		"ineligible": func(a *PolicyFitArtifact) {
			for i := range a.Candidates {
				a.Candidates[i].Eligible = false
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var altered PolicyFitArtifact
			if err := json.Unmarshal(encoded, &altered); err != nil {
				t.Fatal(err)
			}
			mutate(&altered)
			if _, err := ReplayFittedPolicy(v, altered); err == nil {
				t.Fatal("altered artifact applied")
			}
		})
	}
}

func TestPolicyReplayRejectsChangedOrUnboundInputs(t *testing.T) {
	baseline, _ := policyDataset(t)
	encoded, _ := json.Marshal(baseline)
	for name, mutate := range map[string]func(*Dataset){
		"missing raw": func(d *Dataset) { d.Prediction[0].Evaluation = nil },
		"legacy raw":  func(d *Dataset) { d.Prediction[0].Evaluation.MetadataVersion = 0 },
		"partial":     func(d *Dataset) { d.Prediction[0].Evaluation.Coverage = "partial" },
		"model":       func(d *Dataset) { d.Prediction[0].Evaluation.ResolvedModel = "jev-2.0.0" },
		"spec":        func(d *Dataset) { d.Prediction[0].Evaluation.SpecHash = "different" },
		"question hash": func(d *Dataset) {
			for k := range d.Prediction[0].Evaluation.QuestionHashes {
				d.Prediction[0].Evaluation.QuestionHashes[k] = "different"
				break
			}
		},
		"material": func(d *Dataset) {
			d.Samples[0].Material.Primary += " changed"
			d.Samples[0].SourceHash, _ = HashMaterial(*d.Samples[0].Material)
		},
		"wire state":      func(d *Dataset) { d.Prediction[0].Evaluation.WireState = "{}" },
		"call provenance": func(d *Dataset) { d.Prediction[0].Evaluation.Calls = nil },
		"mixed batching":  func(d *Dataset) { d.Prediction[0].Evaluation.BatchSemantics = "another-batching" },
		"taxonomy":        func(d *Dataset) { d.Prediction[0].Evaluation.TaxonomyVersion = "other" },
		"invalid probability": func(d *Dataset) {
			for k, j := range d.Prediction[0].Evaluation.Judgments {
				if j.Noul != nil {
					x := 2.0
					j.Noul = &x
					d.Prediction[0].Evaluation.Judgments[k] = j
					break
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var d Dataset
			if err := json.Unmarshal(encoded, &d); err != nil {
				t.Fatal(err)
			}
			mutate(&d)
			if _, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0"); err == nil {
				t.Fatal("unsafe replay accepted")
			}
		})
	}
	changedCatalog := exportCatalog()
	changedCatalog.Topics[0].Description += " new meaning"
	if _, err := PreparePolicyReplay(baseline, changedCatalog, "jev-1.13.0"); err == nil {
		t.Fatal("changed question semantics reused")
	}
}

func TestPolicyFitRejectsHoldoutLeakageAndMalformedConfig(t *testing.T) {
	d, _ := policyDataset(t)
	for _, split := range []string{"holdout", "test", ""} {
		d.Split = split
		v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := FitProductionPolicy(v, fitConfig()); err == nil {
			t.Fatal("non-fit split accepted")
		}
	}
	d.Split = "train"
	d.Samples[1].SourceHash = d.Samples[0].SourceHash
	d.Samples[1].Material = d.Samples[0].Material
	d.Prediction[1].Evaluation = d.Prediction[0].Evaluation
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FitProductionPolicy(v, fitConfig()); err == nil {
		t.Fatal("duplicated source fitted")
	}
	for name, mutate := range map[string]func(*PolicyFitConfig){
		"calibrated":          func(c *PolicyFitConfig) { c.Base.Calibrated = true },
		"alias":               func(c *PolicyFitConfig) { c.Base.AllowAliasDrift = true },
		"nan":                 func(c *PolicyFitConfig) { c.Accept[0] = math.NaN() },
		"infinite risk":       func(c *PolicyFitConfig) { c.Risk.FalsePositive = math.Inf(1) },
		"missing dimension":   func(c *PolicyFitConfig) { delete(c.MinCoverage, "use") },
		"no error loss":       func(c *PolicyFitConfig) { c.Risk.FalseNegative = 0 },
		"duplicate candidate": func(c *PolicyFitConfig) { c.Accept = append(c.Accept, c.Accept[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			c := fitConfig()
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("bad fit config accepted")
			}
		})
	}
}

func TestVerifiedPolicyReplayDoesNotExposeMutableReferences(t *testing.T) {
	d, _ := policyDataset(t)
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	p := classify.DefaultPolicy()
	first, err := v.Replay(p)
	if err != nil {
		t.Fatal(err)
	}
	expected := first.Samples[0].Material.Primary
	d.Samples[0].Material.Primary = "caller mutation"
	first.Samples[0].Material.Primary = "result mutation"
	second, err := v.Replay(p)
	if err != nil {
		t.Fatal(err)
	}
	if second.Samples[0].Material.Primary != expected {
		t.Fatal("verified input can be changed externally")
	}
	if reflect.DeepEqual(first.Samples, second.Samples) {
		t.Fatal("test did not mutate result")
	}
}

func TestPolicyFitDoesNotRelaxUnsatisfiedConstraints(t *testing.T) {
	d, _ := policyDataset(t)
	v, err := PreparePolicyReplay(d, exportCatalog(), "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	c := fitConfig()
	c.Accept = []float64{0.9}
	c.Choice = []float64{0.9}
	a, err := FitProductionPolicy(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if a.Selected != nil || a.Status != "no_eligible_candidate" || a.Promote {
		t.Fatal("ineligible candidate was selected")
	}
}
