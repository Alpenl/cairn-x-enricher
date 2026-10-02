package evaluation

import "slices"

// legacyReferenceScope is the frozen vocabulary available before granular
// topics were introduced. Missing reviewed_terms preserves that historical
// annotation scope, never the expanding current catalog. Explicit [] means
// only the supplied positive labels were reviewed.
var legacyReferenceScope = map[string][]string{
	"topics": {"llm", "eng", "product", "design", "writing", "invest", "health", "psych", "manage", "media", "law", "edu", "history", "science", "city", "life", "eval",
		"ai_coding", "agent_workflow", "image_creation", "video_creation", "writing_creation", "ui_design", "knowledge_workflow", "information_sources", "model_practice", "creator_business", "finance_resources", "document_layout", "clothing_style"},
	"resource_kinds":    {"skill", "prompt", "software", "component", "model", "reference"},
	"content_functions": {"method", "tool", "case", "data", "opinion"},
	"affordances":       {"quote", "practice", "background", "material"},
}

func reviewedTerm(label Label, dimension, term string) bool {
	if !knownLabel(label) {
		return false
	}
	if slices.Contains(label.Values, term) {
		return true
	}
	if label.ReviewedTerms != nil {
		return slices.Contains(label.ReviewedTerms, term)
	}
	if legacy, ok := legacyReferenceScope[dimension]; ok {
		return slices.Contains(legacy, term)
	}
	return true // mutually exclusive choices retain their historical rubric
}

func reviewedPredictions(label Label, dimension string, predicted []string) []string {
	result := make([]string, 0, len(predicted))
	for _, term := range predicted {
		if reviewedTerm(label, dimension, term) {
			result = append(result, term)
		}
	}
	return result
}

// CandidateRecallMetric separates retrieval omissions from model mistakes.
// It counts known positive topics only; unannotated concepts never contribute
// false negatives or artificial negative probabilities.
type CandidateRecallMetric struct {
	Samples            int     `json:"samples"`
	PositiveTopics     int     `json:"positive_topics"`
	Recalled           int     `json:"recalled"`
	SelectedUnanswered int     `json:"selected_unanswered"`
	NotRecalled        int     `json:"not_recalled"`
	Limited            int     `json:"candidate_limit"`
	Recall             float64 `json:"recall"`
}

func scoreCandidateRecall(metric *CandidateRecallMetric, gold Label, prediction Prediction) {
	if !knownLabel(gold) || prediction.Evaluation == nil || prediction.Evaluation.CandidateManifest == nil {
		return
	}
	metric.Samples++
	raw := prediction.Evaluation
	for _, term := range gold.Values {
		metric.PositiveTopics++
		// CompileSpec freezes topic_<term_id> as the question identity.
		// Selection precedes model inference; an absent provider answer is a
		// separate failure and cannot rewrite the retrieval measurement.
		questionID := "topic_" + term
		if slices.Contains(raw.CandidateManifest.SelectedQuestionIDs, questionID) {
			metric.Recalled++
			if _, answered := raw.Judgments[questionID]; !answered {
				metric.SelectedUnanswered++
			}
			continue
		}
		for _, omitted := range raw.CandidateManifest.Omitted {
			if calibrationDimension(omitted.Dimension) == "topics" && omitted.TermID == term {
				if omitted.Reason == "candidate_limit" {
					metric.Limited++
				} else {
					metric.NotRecalled++
				}
			}
		}
	}
	metric.Recall = ratio(metric.Recalled, metric.PositiveTopics)
}
