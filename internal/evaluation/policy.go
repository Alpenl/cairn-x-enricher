package evaluation

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
)

// Policy is a versioned threshold candidate. It is the experiment-side mirror
// of the production policy so a search can be validated before it is exported.
type Policy struct {
	Version      string  `json:"version"`
	TopicAccept  float64 `json:"topic_accept"`
	TopicReject  float64 `json:"topic_reject"`
	ChoiceAccept float64 `json:"choice_accept"`
	Calibrated   bool    `json:"calibrated"`
}

// Replay re-decides stored topic probabilities under a new policy without any
// model call. It returns the new accepted set and the operations performed, so
// a test can assert that the model call count is exactly zero.
func Replay(policy Policy, probabilities map[string]float64) []string {
	keys := make([]string, 0, len(probabilities))
	for label := range probabilities {
		keys = append(keys, label)
	}
	sort.Strings(keys)
	accepted := []string{}
	for _, label := range keys {
		if probabilities[label] >= policy.TopicAccept {
			accepted = append(accepted, label)
		}
	}
	return accepted
}

// SearchThresholds finds the topic-accept threshold on the training split that
// maximises F1 subject to a coverage floor. It never reads the holdout.
func SearchThresholds(train Dataset, candidates []float64, coverageFloor float64) (Policy, error) {
	if train.Split != "train" && train.Split != "dev" {
		return Policy{}, errors.New("threshold search requires train or dev, never holdout")
	}
	if math.IsNaN(coverageFloor) || coverageFloor < 0 || coverageFloor > 1 {
		return Policy{}, errors.New("invalid coverage floor")
	}
	for _, threshold := range candidates {
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0.2 || threshold > 1 {
			return Policy{}, errors.New("threshold must exceed reject=0.2 and be at most 1")
		}
	}
	if err := train.Validate(); err != nil {
		return Policy{}, err
	}
	if len(candidates) == 0 {
		return Policy{}, errors.New("at least one candidate threshold is required")
	}
	best := Policy{Version: "searched", TopicAccept: 0.8, TopicReject: 0.2, ChoiceAccept: 0.65}
	bestScore := -1.0
	for _, threshold := range candidates {
		policy := best
		policy.TopicAccept = threshold
		// Re-score the training split under this threshold by re-deriving the
		// accepted topics from the stored probabilities.
		candidate := train
		candidate.Prediction = make([]Prediction, 0, len(train.Prediction))
		for _, prediction := range train.Prediction {
			accepted := Replay(policy, prediction.TopicProbabilities)
			updated := prediction
			updated.Topics = accepted
			updated.Abstained = slices.DeleteFunc(append([]string{}, prediction.Abstained...), func(name string) bool { return name == "topic" || name == "topics" })
			for _, probability := range prediction.TopicProbabilities {
				if probability > policy.TopicReject && probability < policy.TopicAccept {
					updated.Abstained = append(updated.Abstained, "topic")
					break
				}
			}
			candidate.Prediction = append(candidate.Prediction, updated)
		}
		report, err := Score(candidate)
		if err != nil {
			return Policy{}, err
		}
		if report.Coverage < coverageFloor {
			continue
		}
		score := 0.0
		for _, metric := range report.Dimensions {
			if metric.Dimension == "topics" {
				score = metric.F1
				break
			}
		}
		if score > bestScore {
			bestScore = score
			best = policy
		}
	}
	if bestScore < 0 {
		return Policy{}, errors.New("no threshold met the coverage floor")
	}
	return best, nil
}

// AblationVariant names one controlled change. Exactly one variable differs
// from the baseline so the comparison is attributable.
type AblationVariant struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Report      Report `json:"report"`
}

// AblationDelta is one dimension's baseline-vs-variant delta. A dimension
// present in only one variant is reported as not comparable rather than
// subtracted.
type AblationDelta struct {
	Dimension  string  `json:"dimension"`
	Baseline   float64 `json:"baseline_f1"`
	Variant    float64 `json:"variant_f1"`
	Delta      float64 `json:"delta"`
	Comparable bool    `json:"comparable"`
	Reason     string  `json:"reason,omitempty"`
}

// CompareAblations compares only supported tasks scored on the same frozen
// references. DatasetHash includes predictions and is expected to differ.
// Comparable establishes metric comparability, not single-variable causality;
// the experiment runner must separately freeze the intervention and inputs.
func CompareAblations(baseline Report, variant Report) []AblationDelta {
	index := func(metrics []DimensionMetric) (map[string]DimensionMetric, map[string]int) {
		values, counts := map[string]DimensionMetric{}, map[string]int{}
		for _, metric := range metrics {
			values[metric.Dimension] = metric
			counts[metric.Dimension]++
		}
		return values, counts
	}
	base, baseCounts := index(baseline.Dimensions)
	other, otherCounts := index(variant.Dimensions)
	names := map[string]bool{}
	for name := range base {
		names[name] = true
	}
	for name := range other {
		names[name] = true
	}
	identityReason := ""
	if !validDigest(baseline.ReferenceHash) || baseline.ReferenceHash != variant.ReferenceHash {
		identityReason = "missing or different frozen references"
	} else if baseline.Split == "" || baseline.Split != variant.Split || baseline.SamplesTotal != variant.SamplesTotal ||
		baseline.KnownReferenceSamples != variant.KnownReferenceSamples || baseline.IndependentGroups != variant.IndependentGroups {
		identityReason = "different evaluation population or split"
	}
	deltas := []AblationDelta{}
	for name := range names {
		b, v := base[name], other[name]
		delta := AblationDelta{Dimension: name, Baseline: b.F1, Variant: v.F1}
		switch {
		case identityReason != "":
			delta.Reason = identityReason
		case baseCounts[name] != 1 || otherCounts[name] != 1:
			delta.Reason = "missing or duplicate dimension"
		case name == "" || b.MultiLabel != v.MultiLabel:
			delta.Reason = "different task semantics"
		case b.Support <= 0 || b.Support != v.Support || b.Unknown != v.Unknown:
			delta.Reason = "missing or different reference support"
		case !unitInterval(b.F1) || !unitInterval(v.F1):
			delta.Reason = "invalid F1"
		default:
			delta.Comparable = true
			delta.Delta = v.F1 - b.F1
		}
		// Invalid input must still produce serializable diagnostic output.
		if !unitInterval(delta.Baseline) {
			delta.Baseline = 0
		}
		if !unitInterval(delta.Variant) {
			delta.Variant = 0
		}
		deltas = append(deltas, delta)
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Dimension < deltas[j].Dimension })
	return deltas
}

// GateThresholds is frozen before the holdout is examined. Changing a threshold
// after seeing the holdout invalidates the evaluation, so the config is part of
// the report.
type GateThresholds struct {
	MinIndependentGroups int     `json:"min_independent_groups"`
	MinMacroRecall       float64 `json:"min_macro_recall"`
	MinCoverage          float64 `json:"min_coverage"`
	MaxAcceptedError     float64 `json:"max_accepted_error"`
	MaxReviewFields      float64 `json:"max_review_fields_per_sample"`
	MinGoldSamples       int     `json:"min_gold_samples"`
	RequireCalibration   bool    `json:"require_calibration"`
	MaxBrier             float64 `json:"max_brier"`
}

// DefaultGate is the conservative baseline. It is explicitly not calibrated
// against a real holdout, so a passing evaluation is necessary but not
// sufficient for promotion.
func DefaultGate() GateThresholds {
	return GateThresholds{
		MinMacroRecall: 0.6, MinCoverage: 0.5, MaxAcceptedError: 0.1,
		MaxReviewFields: 3, MinGoldSamples: minSupportForConclusion, MinIndependentGroups: minSupportForConclusion, MaxBrier: 0.25,
	}
}

func unitInterval(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

// Validate rejects malformed frozen gates before any live inference can run.
// Zero error/Brier limits are valid strict limits, never a disabling sentinel.
func (gate GateThresholds) Validate() error {
	if gate.MinGoldSamples <= 0 || gate.MinIndependentGroups <= 0 {
		return errors.New("gate requires positive sample and independent-group floors")
	}
	for _, value := range []float64{gate.MinMacroRecall, gate.MinCoverage, gate.MaxAcceptedError, gate.MaxBrier} {
		if !unitInterval(value) {
			return errors.New("gate probability thresholds must be finite and in [0,1]")
		}
	}
	if math.IsNaN(gate.MaxReviewFields) || math.IsInf(gate.MaxReviewFields, 0) || gate.MaxReviewFields < 0 || gate.MaxReviewFields > 6 {
		return errors.New("gate review-field limit must be finite and in [0,6]")
	}
	return nil
}

// PromotionDecision is the result of applying the gate.
type PromotionDecision struct {
	Promote      bool     `json:"promote"`
	Reasons      []string `json:"reasons,omitempty"`
	Blockers     []string `json:"blockers,omitempty"`
	Inconclusive bool     `json:"inconclusive"`
}

// EvaluateGate applies the frozen thresholds. A zero-tolerance safety failure
// blocks promotion regardless of statistical quality, and a small sample makes
// the result inconclusive rather than passing.
func EvaluateGate(report Report, gate GateThresholds) PromotionDecision {
	decision := PromotionDecision{Promote: true, Inconclusive: report.Inconclusive}
	if err := gate.Validate(); err != nil {
		return PromotionDecision{Promote: false, Inconclusive: true, Blockers: []string{err.Error()}}
	}
	decision.Reasons = append(decision.Reasons, report.InconclusiveWhy...)
	if report.Split != "holdout" {
		decision.Blockers = append(decision.Blockers, "promotion requires the frozen holdout split")
	}
	if !validDigest(report.ReferenceHash) || !validDigest(report.DatasetHash) {
		decision.Blockers = append(decision.Blockers, "missing or invalid reference/dataset identity")
	}
	if report.SamplesTotal <= 0 || report.SamplesWithGold < 0 || report.SamplesWithGold > report.SamplesTotal ||
		report.SamplesMissing < 0 || report.SamplesMissing != report.SamplesTotal-report.SamplesWithGold ||
		report.MissingGoldCount < 0 || report.MissingGoldCount > report.SamplesWithGold ||
		report.KnownReferenceSamples <= 0 || report.KnownReferenceSamples > report.SamplesWithGold ||
		report.IndependentGroups <= 0 || report.IndependentGroups > report.KnownReferenceSamples ||
		report.KnownFields < report.KnownReferenceSamples || report.KnownFields > 6*report.KnownReferenceSamples ||
		report.DecidedFields < 0 || report.DecidedFields > report.KnownFields ||
		math.Abs(report.Coverage-ratio(report.DecidedFields, report.KnownFields)) > 1e-9 {
		decision.Blockers = append(decision.Blockers, "inconsistent report counts or coverage")
	}
	if report.SamplesMissing > 0 || report.MissingGoldCount > 0 {
		decision.Inconclusive = true
		decision.Reasons = append(decision.Reasons, "reference or predictions missing")
	}
	for _, value := range []float64{report.MacroRecall, report.Coverage, report.AcceptedError, report.Brier, report.ECE} {
		if !unitInterval(value) {
			decision.Blockers = append(decision.Blockers, "invalid metric")
			break
		}
	}
	if math.IsNaN(report.ReviewFields) || math.IsInf(report.ReviewFields, 0) || report.ReviewFields < 0 || report.ReviewFields > 6 {
		decision.Blockers = append(decision.Blockers, "invalid review-field metric")
	}
	if report.KnownReferenceSamples < gate.MinGoldSamples || report.IndependentGroups < gate.MinIndependentGroups {
		decision.Inconclusive = true
		decision.Reasons = append(decision.Reasons, "insufficient gold samples for a conclusion")
	}
	if report.MacroRecall < gate.MinMacroRecall {
		decision.Blockers = append(decision.Blockers, fmt.Sprintf("macro recall %.3f below %.3f", report.MacroRecall, gate.MinMacroRecall))
	}
	if report.Coverage < gate.MinCoverage {
		decision.Blockers = append(decision.Blockers, fmt.Sprintf("coverage %.3f below %.3f", report.Coverage, gate.MinCoverage))
	}
	// Accepted error is a safety metric: a wrong accepted tag is worse than an
	// abstention, so it is checked independently of recall.
	if report.AcceptedError > gate.MaxAcceptedError {
		decision.Blockers = append(decision.Blockers, fmt.Sprintf("accepted error %.3f above %.3f", report.AcceptedError, gate.MaxAcceptedError))
	}
	if report.ReviewFields > gate.MaxReviewFields {
		decision.Blockers = append(decision.Blockers, fmt.Sprintf("review burden %.2f above %.2f", report.ReviewFields, gate.MaxReviewFields))
	}
	if gate.RequireCalibration && !report.HasCalibration {
		decision.Blockers = append(decision.Blockers, "calibration evidence is required but missing")
	}
	if report.HasCalibration && report.Brier > gate.MaxBrier {
		decision.Blockers = append(decision.Blockers, fmt.Sprintf("Brier %.3f above %.3f", report.Brier, gate.MaxBrier))
	}
	if len(decision.Blockers) > 0 {
		decision.Promote = false
	}
	if decision.Inconclusive {
		// An inconclusive result is never an automatic promotion.
		decision.Promote = false
	}
	return decision
}

// ModelDrift compares two recorded concrete model identities.
// A drifted alias must be re-evaluated rather than inheriting the old
// calibration.
type ModelDrift struct {
	PreviousResolved string `json:"previous_resolved"`
	Resolved         string `json:"resolved"`
	Drifted          bool   `json:"drifted"`
	Reevaluate       bool   `json:"reevaluate"`
}

// DetectModelDrift compares actual resolutions across runs, never alias spelling.
// Missing identity is unknown and requires evaluation rather than inferred reuse.
func DetectModelDrift(previousResolved, resolved string) ModelDrift {
	drifted := previousResolved != "" && resolved != "" && previousResolved != resolved
	return ModelDrift{PreviousResolved: previousResolved, Resolved: resolved, Drifted: drifted, Reevaluate: drifted || previousResolved == "" || resolved == ""}
}
