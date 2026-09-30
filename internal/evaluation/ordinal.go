package evaluation

import (
	"errors"
	"math"
	"strconv"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// OrdinalMetric retains the full ordered distribution, rather than treating two
// equal means as identical evidence. It is one observation, not a quality gate.
type OrdinalMetric struct {
	ExpectedLevel          int       `json:"expected_level"`
	Levels                 []string  `json:"levels"`
	Probabilities          []float64 `json:"probabilities"`
	ReportedScore          float64   `json:"reported_score"`
	DistributionMean       float64   `json:"distribution_mean"`
	MeanDiscrepancy        float64   `json:"mean_discrepancy"`
	AbsoluteError          float64   `json:"absolute_error"`
	ExpectedAbsoluteError  float64   `json:"expected_absolute_error"`
	RankedProbabilityScore float64   `json:"normalized_ranked_probability_score"`
	Confidence             *float64  `json:"confidence"`
}

// ScoreOrdinal measures a validated typed Score against one explicit ordinal
// reference. RPS averages squared cumulative errors over K-1 ordered cutpoints.
// Callers must separately establish source/spec and reference provenance; this
// mathematical helper does not manufacture a reference or a promotion decision.
func ScoreOrdinal(question classify.Question, answer classify.RawAnswer, expected int) (OrdinalMetric, error) {
	if question.Kind != classify.QuestionScore || expected < 0 || expected >= len(question.AnswerOptions()) {
		return OrdinalMetric{}, errors.New("ordinal metric needs a Score question and an in-range reference level")
	}
	if err := question.Validate(); err != nil {
		return OrdinalMetric{}, err
	}
	if err := classify.ValidateAnswers(classify.QuestionSpec{Questions: []classify.Question{question}}, map[string]classify.RawAnswer{question.ID: answer}); err != nil {
		return OrdinalMetric{}, err
	}
	count := len(question.AnswerOptions())
	if count < 2 {
		return OrdinalMetric{}, errors.New("ordinal metric needs at least two ordered levels")
	}
	m := OrdinalMetric{ExpectedLevel: expected, ReportedScore: answer.Score.Score, AbsoluteError: math.Abs(answer.Score.Score - float64(expected)), Levels: make([]string, count), Probabilities: make([]float64, count)}
	if answer.Confidence != nil {
		confidence := *answer.Confidence
		m.Confidence = &confidence
	}
	cumulative := 0.0
	for i := 0; i < count; i++ {
		key := strconv.Itoa(i)
		p := answer.Score.Probabilities[key]
		m.Levels[i], m.Probabilities[i] = answer.Score.Legend[key], p
		m.DistributionMean += float64(i) * p
		m.ExpectedAbsoluteError += math.Abs(float64(i-expected)) * p
		cumulative += p
		if i < count-1 {
			m.RankedProbabilityScore += math.Pow(cumulative-boolToFloat(expected <= i), 2) / float64(count-1)
		}
	}
	m.MeanDiscrepancy = math.Abs(m.DistributionMean - m.ReportedScore)
	return m, nil
}
