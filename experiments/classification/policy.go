package classification

import (
	"errors"
	"fmt"
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
			candidate.Prediction = append(candidate.Prediction, updated)
		}
		report, err := Score(candidate)
		if err != nil {
			return Policy{}, err
		}
		if report.Coverage < coverageFloor {
			continue
		}
		score := macroF1(report)
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

func macroF1(report Report) float64 {
	total := 0.0
	count := 0
	for _, metric := range report.Dimensions {
		total += metric.F1
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
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
}

// CompareAblations returns each variant dimension's delta against the baseline.
func CompareAblations(baseline Report, variant Report) []AblationDelta {
	base := map[string]float64{}
	for _, metric := range baseline.Dimensions {
		base[metric.Dimension] = metric.F1
	}
	deltas := []AblationDelta{}
	for _, metric := range variant.Dimensions {
		baseValue, ok := base[metric.Dimension]
		deltas = append(deltas, AblationDelta{
			Dimension: metric.Dimension, Baseline: baseValue, Variant: metric.F1,
			Delta: metric.F1 - baseValue, Comparable: ok,
		})
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Dimension < deltas[j].Dimension })
	return deltas
}

// GateThresholds is frozen before the holdout is examined. Changing a threshold
// after seeing the holdout invalidates the evaluation, so the config is part of
// the report.
type GateThresholds struct {
	MinMacroRecall     float64 `json:"min_macro_recall"`
	MinCoverage        float64 `json:"min_coverage"`
	MaxAcceptedError   float64 `json:"max_accepted_error"`
	MaxReviewFields    float64 `json:"max_review_fields_per_sample"`
	MinGoldSamples     int     `json:"min_gold_samples"`
	RequireCalibration bool    `json:"require_calibration"`
	MaxBrier           float64 `json:"max_brier"`
}

// DefaultGate is the conservative baseline. It is explicitly not calibrated
// against a real holdout, so a passing evaluation is necessary but not
// sufficient for promotion.
func DefaultGate() GateThresholds {
	return GateThresholds{
		MinMacroRecall: 0.6, MinCoverage: 0.5, MaxAcceptedError: 0.1,
		MaxReviewFields: 3, MinGoldSamples: minSupportForConclusion, MaxBrier: 0.25,
	}
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
	decision := PromotionDecision{Promote: true}
	if report.SamplesWithGold < gate.MinGoldSamples {
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
	if report.HasCalibration && gate.MaxBrier > 0 && report.Brier > gate.MaxBrier {
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

// ModelDrift reports whether a resolved model differs from the requested one.
// A drifted alias must be re-evaluated rather than inheriting the old
// calibration.
type ModelDrift struct {
	Requested  string `json:"requested"`
	Resolved   string `json:"resolved"`
	Drifted    bool   `json:"drifted"`
	Reevaluate bool   `json:"reevaluate"`
}

// DetectModelDrift reports whether the resolved model differs from the request.
func DetectModelDrift(requested, resolved string) ModelDrift {
	drifted := requested != "" && resolved != "" && requested != resolved
	return ModelDrift{Requested: requested, Resolved: resolved, Drifted: drifted, Reevaluate: drifted}
}
