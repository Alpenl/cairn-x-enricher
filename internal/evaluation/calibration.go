package evaluation

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// CalibrationConfig fixes descriptive diagnostics; it never selects a policy.
type CalibrationConfig struct {
	Version string          `json:"version"`
	Bins    int             `json:"bins"`
	Policy  classify.Policy `json:"policy"`
	Cutoffs []float64       `json:"cutoffs"`
}

// Validate rejects invalid or unbounded diagnostic grids.
func (c CalibrationConfig) Validate() error {
	if c.Version == "" || c.Bins < 2 || c.Bins > 50 || len(c.Cutoffs) == 0 || len(c.Cutoffs) > 20 {
		return errors.New("calibration requires a version, 2..50 bins and 1..20 cutoffs")
	}
	if err := c.Policy.Validate(); err != nil {
		return err
	}
	for i, cutoff := range c.Cutoffs {
		if !finiteUnit(cutoff) || (i > 0 && cutoff <= c.Cutoffs[i-1]) {
			return errors.New("calibration cutoffs must be finite, increasing and in [0,1]")
		}
	}
	return nil
}

// ReliabilityBin uses [lower, upper), except that the last bin includes 1.
// Empty bins retain null means instead of inventing zero observed accuracy.
type ReliabilityBin struct {
	Lower           float64  `json:"lower"`
	Upper           float64  `json:"upper"`
	Count           int      `json:"count"`
	Positive        int      `json:"positive_outcomes"`
	MeanProbability *float64 `json:"mean_probability"`
	ObservedRate    *float64 `json:"observed_rate"`
}

// ProbabilityMetric reports raw probabilities, including policy abstentions.
type ProbabilityMetric struct {
	Name               string                `json:"name"`
	Kind               classify.QuestionKind `json:"kind"`
	Observations       int                   `json:"observations"`
	IndependentGroups  int                   `json:"independent_groups"`
	UnknownReference   int                   `json:"unknown_reference"`
	AmbiguousReference int                   `json:"ambiguous_reference"`
	BrierScale         string                `json:"brier_scale"`
	ReliabilityTarget  string                `json:"reliability_target"`
	Brier              *float64              `json:"brier"`
	ECE                *float64              `json:"ece"`
	Bins               []ReliabilityBin      `json:"bins"`
	Inconclusive       bool                  `json:"inconclusive"`
}

// SelectiveRiskPoint adds one cutoff to the actual production Choice verdict.
// A missing feature is excluded explicitly; confidence is never a probability.
type SelectiveRiskPoint struct {
	Dimension      string   `json:"dimension"`
	Feature        string   `json:"feature"`
	Cutoff         float64  `json:"cutoff"`
	Support        int      `json:"support"`
	Accepted       int      `json:"accepted"`
	Errors         int      `json:"errors"`
	MissingFeature int      `json:"missing_feature"`
	Coverage       float64  `json:"coverage"`
	AcceptedError  *float64 `json:"accepted_error"`
}

// CalibrationArtifact is descriptive evidence, not a calibrated or promoted policy.
type CalibrationArtifact struct {
	Version                 string               `json:"version"`
	DatasetHash             string               `json:"dataset_hash"`
	ReferenceHash           string               `json:"reference_hash"`
	Split                   string               `json:"split"`
	Binding                 ReplayBinding        `json:"binding"`
	Config                  CalibrationConfig    `json:"config"`
	ModelCalls              int                  `json:"model_calls"`
	Promote                 bool                 `json:"promote"`
	Calibrated              bool                 `json:"calibrated"`
	Samples                 int                  `json:"samples"`
	IndependentGroups       int                  `json:"independent_groups"`
	ReferenceProvenance     map[Provenance]int   `json:"reference_provenance"`
	Dimensions              []ProbabilityMetric  `json:"dimensions"`
	Questions               []ProbabilityMetric  `json:"questions"`
	ChoiceRisk              []SelectiveRiskPoint `json:"choice_risk"`
	ScoreObservations       int                  `json:"score_observations"`
	OrdinalQualityEvaluated bool                 `json:"ordinal_quality_evaluated"`
	Limitations             []string             `json:"limitations"`
}

type probabilityAccumulator struct {
	metric ProbabilityMetric
	points []calibPoint
	brier  float64
	groups map[string]bool
}

type choiceObservation struct {
	dimension           string
	accepted, correct   bool
	probability, margin float64
	confidence          *float64
}

func finiteUnit(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= 1 }

func calibrationDimension(value string) string {
	// The topic compiler retains its historical singular dimension ID.
	if value == "topic" {
		return "topics"
	}
	return value
}

func finishProbability(a *probabilityAccumulator, bins int) ProbabilityMetric {
	m := a.metric
	m.Observations, m.IndependentGroups = len(a.points), len(a.groups)
	m.Inconclusive = m.Observations < minSupportForConclusion || m.IndependentGroups < minSupportForConclusion
	m.Bins = make([]ReliabilityBin, bins)
	sums := make([]float64, bins)
	for i := range m.Bins {
		m.Bins[i].Lower = float64(i) / float64(bins)
		m.Bins[i].Upper = float64(i+1) / float64(bins)
	}
	for _, point := range a.points {
		i := min(int(point.p*float64(bins)), bins-1)
		m.Bins[i].Count++
		if point.correct {
			m.Bins[i].Positive++
		}
		sums[i] += point.p
	}
	if len(a.points) == 0 {
		return m
	}
	brier, ece := a.brier/float64(len(a.points)), 0.0
	for i := range m.Bins {
		b := &m.Bins[i]
		if b.Count == 0 {
			continue
		}
		mean, observed := sums[i]/float64(b.Count), float64(b.Positive)/float64(b.Count)
		b.MeanProbability, b.ObservedRate = &mean, &observed
		ece += float64(b.Count) / float64(len(a.points)) * math.Abs(mean-observed)
	}
	m.Brier, m.ECE = &brier, &ece
	return m
}

// Calibration consumes only records accepted by PreparePolicyReplay. It reports
// train/dev diagnostics; this exploratory interface cannot inspect holdout.
func (v *VerifiedReplay) Calibration(config CalibrationConfig) (CalibrationArtifact, error) {
	if v == nil {
		return CalibrationArtifact{}, errors.New("missing verified replay")
	}
	if v.dataset.Split != "train" && v.dataset.Split != "dev" {
		return CalibrationArtifact{}, errors.New("calibration diagnostics require train or dev, never holdout")
	}
	if err := config.Validate(); err != nil {
		return CalibrationArtifact{}, err
	}
	report := CalibrationArtifact{Version: "probability-diagnostics-v1", Split: v.dataset.Split, Binding: v.binding, Config: config, Samples: len(v.dataset.Samples), ReferenceProvenance: map[Provenance]int{}, Limitations: []string{
		"descriptive train/dev analysis; no fitting, selection, calibration claim or promotion",
		"Noul Brier is binary [0,1]; Choice Brier is multiclass sum [0,2]; do not average these scales",
		"Noul reliability uses statement truth; Choice reliability uses chosen-option correctness",
		"unknown references and ambiguous Choice alternatives are excluded, not invented as negatives or uniform targets",
		"pooled Noul observations share samples and groups; rare-label and reference-scope bias remains",
		"fixed-width ECE depends on binning; no uncertainty interval or confidence-as-probability claim",
		"rounded raw probabilities are not renormalized; the ideal multiclass Brier range assumes unit mass",
		"risk coverage denominator is known unambiguous Choice observations, not all collection fields",
		"no ordinal reference corpus or Score-enabled evaluations in this diagnostic interface",
	}}
	var err error
	report.DatasetHash, err = HashDataset(v.dataset)
	if err != nil {
		return CalibrationArtifact{}, err
	}
	report.ReferenceHash, err = HashReference(v.dataset)
	if err != nil {
		return CalibrationArtifact{}, err
	}
	byID := map[string]Prediction{}
	for _, p := range v.dataset.Prediction {
		byID[p.SampleID] = p
	}
	questions, dimensions := map[string]*probabilityAccumulator{}, map[string]*probabilityAccumulator{}
	groups := map[string]bool{}
	var choices []choiceObservation
	get := func(target map[string]*probabilityAccumulator, name string, kind classify.QuestionKind) *probabilityAccumulator {
		if a := target[name]; a != nil {
			return a
		}
		a := &probabilityAccumulator{metric: ProbabilityMetric{Name: name, Kind: kind, BrierScale: "binary_[0,1]", ReliabilityTarget: "statement_truth"}, groups: map[string]bool{}}
		if kind == classify.QuestionChoice {
			a.metric.BrierScale = "multiclass_sum_[0,2]"
			a.metric.ReliabilityTarget = "chosen_option_correctness"
		}
		target[name] = a
		return a
	}
	for _, sample := range v.dataset.Samples {
		group := sample.GroupID
		if group == "" {
			group = sample.SampleID
		}
		groups[group] = true
		report.ReferenceProvenance[sample.Provenance]++
		raw := *byID[sample.SampleID].Evaluation
		decided, err := classify.Decide(raw, config.Policy)
		if err != nil {
			return CalibrationArtifact{}, err
		}
		accepted := map[string]bool{}
		for _, d := range decided.Decisions {
			if d.Verdict == classify.VerdictAccepted {
				accepted[d.Dimension] = true
			}
		}
		labels := map[string]Label{}
		if sample.Gold != nil {
			labels = referenceLabels(*sample.Gold)
		}
		for dimension, label := range labels {
			if !knownLabel(label) || (dimension != "topics" && dimension != "content_functions" && dimension != "affordances") {
				continue
			}
			for _, term := range label.Values {
				found := false
				for _, judgment := range raw.Judgments {
					if calibrationDimension(judgment.Dimension) == dimension && judgment.TermID == term && judgment.Kind == classify.QuestionNoul {
						found = true
					}
				}
				if !found {
					return CalibrationArtifact{}, fmt.Errorf("reference term in %s is absent from the question set", dimension)
				}
			}
		}
		ids := make([]string, 0, len(raw.Judgments))
		for id := range raw.Judgments {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			j := raw.Judgments[id]
			j.Dimension = calibrationDimension(j.Dimension)
			if j.Kind == classify.QuestionScore {
				report.ScoreObservations++
				continue
			}
			if j.Kind != classify.QuestionNoul && j.Kind != classify.QuestionChoice {
				return CalibrationArtifact{}, fmt.Errorf("unsupported judgment kind %s", j.Kind)
			}
			q, d := get(questions, id, j.Kind), get(dimensions, j.Dimension, j.Kind)
			label := labels[j.Dimension]
			if !knownLabel(label) {
				q.metric.UnknownReference++
				d.metric.UnknownReference++
				continue
			}
			if j.Kind == classify.QuestionChoice && len(label.Values) > 1 {
				q.metric.AmbiguousReference++
				d.metric.AmbiguousReference++
				continue
			}
			var p, loss float64
			var correct bool
			if j.Kind == classify.QuestionNoul {
				p, correct = *j.Noul, slices.Contains(label.Values, j.TermID)
				loss = math.Pow(p-boolToFloat(correct), 2)
			} else {
				expected := "none"
				if len(label.Values) == 1 {
					expected = label.Values[0]
				}
				if _, ok := j.Probabilities[expected]; !ok {
					return CalibrationArtifact{}, fmt.Errorf("reference for %s is absent from its candidate set", id)
				}
				options := make([]string, 0, len(j.Probabilities))
				for option := range j.Probabilities {
					options = append(options, option)
				}
				sort.Strings(options)
				for _, option := range options {
					loss += math.Pow(j.Probabilities[option]-boolToFloat(option == expected), 2)
				}
				p, correct = j.Probabilities[j.Choice], j.Choice == expected
				features := classify.DescribeDistribution(j.Probabilities)
				choices = append(choices, choiceObservation{dimension: j.Dimension, accepted: accepted[j.Dimension], correct: correct, probability: p, margin: features.Margin, confidence: j.Confidence})
			}
			for _, a := range []*probabilityAccumulator{q, d} {
				a.points = append(a.points, calibPoint{p: p, correct: correct})
				a.brier += loss
				a.groups[group] = true
			}
		}
	}
	report.IndependentGroups = len(groups)
	for _, collection := range []struct {
		source map[string]*probabilityAccumulator
		output *[]ProbabilityMetric
	}{{dimensions, &report.Dimensions}, {questions, &report.Questions}} {
		keys := make([]string, 0, len(collection.source))
		for key := range collection.source {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			*collection.output = append(*collection.output, finishProbability(collection.source[key], config.Bins))
		}
	}
	for _, dimension := range []string{"all_choices", "carriers", "form", "use"} {
		for _, feature := range []string{"production_policy", "chosen_probability", "margin", "confidence"} {
			cutoffs := config.Cutoffs
			if feature == "production_policy" {
				cutoffs = []float64{0}
			}
			for _, cutoff := range cutoffs {
				point := SelectiveRiskPoint{Dimension: dimension, Feature: feature, Cutoff: cutoff}
				for _, c := range choices {
					if dimension != "all_choices" && dimension != c.dimension {
						continue
					}
					point.Support++
					value := 1.0
					switch feature {
					case "chosen_probability":
						value = c.probability
					case "margin":
						value = c.margin
					case "confidence":
						if c.confidence == nil {
							point.MissingFeature++
							continue
						}
						value = *c.confidence
					}
					if c.accepted && value >= cutoff {
						point.Accepted++
						if !c.correct {
							point.Errors++
						}
					}
				}
				if point.Support > 0 {
					point.Coverage = float64(point.Accepted) / float64(point.Support)
				}
				if point.Accepted > 0 {
					risk := float64(point.Errors) / float64(point.Accepted)
					point.AcceptedError = &risk
				}
				report.ChoiceRisk = append(report.ChoiceRisk, point)
			}
		}
	}
	report.Config.Cutoffs = slices.Clone(config.Cutoffs)
	return report, nil
}
