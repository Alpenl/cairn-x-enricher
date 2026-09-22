package evaluation

import (
	"errors"
	"math"
	"sort"
)

// DimensionMetric reports per-dimension quality. Multi-label dimensions are
// reported with precision/recall, not accuracy: applying single-class accuracy
// to a multi-label task would make a large abstention look good.
type DimensionMetric struct {
	Dimension    string  `json:"dimension"`
	MultiLabel   bool    `json:"multi_label"`
	TruePositive int     `json:"true_positive"`
	FalsePos     int     `json:"false_positive"`
	FalseNeg     int     `json:"false_negative"`
	Abstained    int     `json:"abstained"`
	Support      int     `json:"support"`
	Precision    float64 `json:"precision"`
	Recall       float64 `json:"recall"`
	F1           float64 `json:"f1"`
}

// Report is the machine-readable evaluation result.
type Report struct {
	DatasetHash      string            `json:"dataset_hash"`
	DatasetName      string            `json:"dataset_name"`
	Split            string            `json:"split"`
	SamplesTotal     int               `json:"samples_total"`
	SamplesWithGold  int               `json:"samples_with_gold"`
	SamplesMissing   int               `json:"samples_missing_gold"`
	Dimensions       []DimensionMetric `json:"dimensions"`
	MicroPrecision   float64           `json:"micro_precision"`
	MicroRecall      float64           `json:"micro_recall"`
	MacroPrecision   float64           `json:"macro_precision"`
	MacroRecall      float64           `json:"macro_recall"`
	AcceptedError    float64           `json:"accepted_error_rate"`
	Coverage         float64           `json:"coverage"`
	ReviewFields     float64           `json:"review_fields_per_sample"`
	Brier            float64           `json:"brier,omitempty"`
	ECE              float64           `json:"ece,omitempty"`
	CalibrationBins  int               `json:"calibration_bins,omitempty"`
	HasCalibration   bool              `json:"has_calibration"`
	Inconclusive     bool              `json:"inconclusive"`
	InconclusiveWhy  []string          `json:"inconclusive_reasons,omitempty"`
	ByLanguage       map[string]int    `json:"by_language"`
	ByLengthBucket   map[string]int    `json:"by_length_bucket"`
	ByCarrier        map[string]int    `json:"by_carrier"`
	MissingGoldCount int               `json:"missing_gold_count"`
}

// minSupportForConclusion is the sample count below which a metric is reported
// but the report is marked inconclusive rather than presented as a result.
const minSupportForConclusion = 20

// Score computes the offline metrics. It is a pure function over validated
// data; it makes no network call and reads no clock.
func Score(dataset Dataset) (Report, error) {
	if err := dataset.Validate(); err != nil {
		return Report{}, err
	}
	hash, err := HashDataset(dataset)
	if err != nil {
		return Report{}, err
	}
	predictions := map[string]Prediction{}
	for _, prediction := range dataset.Prediction {
		predictions[prediction.SampleID] = prediction
	}
	report := Report{
		DatasetHash: hash, DatasetName: dataset.Name, Split: dataset.Split,
		SamplesTotal: len(dataset.Samples),
		ByLanguage:   map[string]int{}, ByLengthBucket: map[string]int{}, ByCarrier: map[string]int{},
	}
	dimensions := map[string]*DimensionMetric{
		"topics":            {Dimension: "topics", MultiLabel: true},
		"content_functions": {Dimension: "content_functions", MultiLabel: true},
		"carriers":          {Dimension: "carriers", MultiLabel: true},
		"affordances":       {Dimension: "affordances", MultiLabel: true},
		"form":              {Dimension: "form"},
		"use":               {Dimension: "use"},
	}
	var acceptedTotal, acceptedWrong int
	var reviewFieldsTotal float64
	var brierSum float64
	var brierCount int
	var calibPoints []calibPoint

	for _, sample := range dataset.Samples {
		report.ByLanguage[NormalizeLanguage(sample.Language)]++
		report.ByLengthBucket[sample.LengthBucket]++
		report.ByCarrier[sample.Carrier]++
		if sample.Gold == nil {
			report.SamplesMissing++
			continue
		}
		report.SamplesWithGold++
		prediction, ok := predictions[sample.SampleID]
		if !ok {
			// A gold sample without a prediction is a coverage gap, not a
			// correct abstention.
			report.MissingGoldCount++
			continue
		}
		// Multi-label dimensions.
		evaluateSet(dimensions["topics"], sample.Gold.Topics.Values, prediction.Topics, dimensionAbstained(prediction, "topic"))
		evaluateSet(dimensions["content_functions"], sample.Gold.ContentFunctions.Values, prediction.ContentFunctions, dimensionAbstained(prediction, "content_function"))
		evaluateSet(dimensions["carriers"], sample.Gold.Carriers.Values, prediction.Carriers, dimensionAbstained(prediction, "carrier"))
		evaluateSet(dimensions["affordances"], sample.Gold.Affordances.Values, prediction.Affordances, dimensionAbstained(prediction, "affordance"))
		// Single-valued dimensions.
		evaluateSet(dimensions["form"], sample.Gold.Form.Values, nonEmpty(prediction.Form), dimensionAbstained(prediction, "form"))
		evaluateSet(dimensions["use"], sample.Gold.Use.Values, nonEmpty(prediction.Use), dimensionAbstained(prediction, "use"))

		for _, accepted := range [][]string{prediction.Topics, prediction.ContentFunctions, prediction.Carriers, prediction.Affordances} {
			acceptedTotal += len(accepted)
		}
		if prediction.Form != "" {
			acceptedTotal++
		}
		if prediction.Use != "" {
			acceptedTotal++
		}
		if !prediction.FormMatches(sample.Gold.Form) && prediction.Form != "" {
			acceptedWrong++
		}
		reviewFieldsTotal += float64(len(prediction.Abstained))

		// Calibration over the topic distribution when present.
		if len(prediction.TopicProbabilities) > 0 {
			labels := make([]string, 0, len(prediction.TopicProbabilities))
			for label := range prediction.TopicProbabilities {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			for _, label := range labels {
				probability := prediction.TopicProbabilities[label]
				correct := contains(sample.Gold.Topics.Values, label)
				brierSum += (probability - boolToFloat(correct)) * (probability - boolToFloat(correct))
				brierCount++
				calibPoints = append(calibPoints, calibPoint{p: probability, correct: correct})
			}
		}
	}

	order := []string{"topics", "content_functions", "carriers", "affordances", "form", "use"}
	var microTP, microFP, microFN int
	var macroP, macroR float64
	scored := 0
	for _, name := range order {
		metric := dimensions[name]
		finalize(metric)
		report.Dimensions = append(report.Dimensions, *metric)
		microTP += metric.TruePositive
		microFP += metric.FalsePos
		microFN += metric.FalseNeg
		if metric.Support > 0 {
			macroP += metric.Precision
			macroR += metric.Recall
			scored++
		}
	}
	report.MicroPrecision = ratio(microTP, microTP+microFP)
	report.MicroRecall = ratio(microTP, microTP+microFN)
	if scored > 0 {
		report.MacroPrecision = macroP / float64(scored)
		report.MacroRecall = macroR / float64(scored)
	}
	report.AcceptedError = ratio(acceptedWrong, acceptedTotal)
	report.Coverage = ratio(acceptedTotal, acceptedTotal+totalAbstained(&report))
	if report.SamplesWithGold+report.SamplesMissing > 0 {
		report.ReviewFields = reviewFieldsTotal / float64(report.SamplesWithGold)
	}
	if brierCount > 0 {
		report.Brier = brierSum / float64(brierCount)
		ece, bins := expectedCalibrationError(calibPoints, 10)
		report.ECE = ece
		report.CalibrationBins = bins
		report.HasCalibration = true
	}

	// A report is inconclusive when support is too small to conclude anything,
	// or when gold is missing. It is never silently presented as a result.
	if report.SamplesWithGold < minSupportForConclusion {
		report.Inconclusive = true
		report.InconclusiveWhy = append(report.InconclusiveWhy, "insufficient gold samples")
	}
	if report.SamplesMissing > 0 {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "missing gold for some samples")
	}
	if report.MissingGoldCount > 0 {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "gold without a prediction")
	}
	sort.Strings(report.InconclusiveWhy)
	return report, nil
}

// FormMatches reports whether a predicted single value is in the acceptable set.
func (p Prediction) FormMatches(label Label) bool {
	if label.Unknown || label.NotApplicable {
		return p.Form == ""
	}
	return contains(label.Values, p.Form)
}

func evaluateSet(metric *DimensionMetric, gold, predicted []string, abstained bool) {
	if len(gold) == 0 {
		// A not_applicable or empty gold is still counted as support so an
		// empty prediction is rewarded rather than ignored.
		metric.Support++
		if len(predicted) > 0 {
			metric.FalsePos += len(predicted)
		}
		return
	}
	metric.Support++
	if abstained && len(predicted) == 0 {
		metric.Abstained++
		metric.FalseNeg += len(gold)
		return
	}
	for _, value := range predicted {
		if contains(gold, value) {
			metric.TruePositive++
		} else {
			metric.FalsePos++
		}
	}
	for _, value := range gold {
		if !contains(predicted, value) {
			metric.FalseNeg++
		}
	}
}

func finalize(metric *DimensionMetric) {
	metric.Precision = ratio(metric.TruePositive, metric.TruePositive+metric.FalsePos)
	metric.Recall = ratio(metric.TruePositive, metric.TruePositive+metric.FalseNeg)
	if metric.Precision+metric.Recall > 0 {
		metric.F1 = 2 * metric.Precision * metric.Recall / (metric.Precision + metric.Recall)
	}
}

// ratio defines the zero-denominator rule explicitly: 0/0 is reported as 0 and
// never as a perfect score.
func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func totalAbstained(report *Report) int {
	total := 0
	for _, metric := range report.Dimensions {
		total += metric.Abstained
	}
	return total
}

func dimensionAbstained(prediction Prediction, dimension string) bool {
	return contains(prediction.Abstained, dimension)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// calibPoint is one (probability, correctness) observation for ECE.
type calibPoint struct {
	p       float64
	correct bool
}

// expectedCalibrationError bins predictions and reports the ECE. It returns the
// number of non-empty bins so a report can disclose the binning limitation.
func expectedCalibrationError(points []calibPoint, bins int) (float64, int) {
	if len(points) == 0 || bins < 1 {
		return 0, 0
	}
	binCorrect := make([]int, bins)
	binTotal := make([]int, bins)
	binSum := make([]float64, bins)
	for _, point := range points {
		index := int(point.p * float64(bins))
		if index >= bins {
			index = bins - 1
		}
		if index < 0 {
			index = 0
		}
		binTotal[index]++
		if point.correct {
			binCorrect[index]++
		}
		binSum[index] += point.p
	}
	ece := 0.0
	nonEmpty := 0
	total := float64(len(points))
	for index := 0; index < bins; index++ {
		if binTotal[index] == 0 {
			continue
		}
		nonEmpty++
		accuracy := float64(binCorrect[index]) / float64(binTotal[index])
		confidence := binSum[index] / float64(binTotal[index])
		ece += (float64(binTotal[index]) / total) * math.Abs(accuracy-confidence)
	}
	return ece, nonEmpty
}

// ErrMismatchedRuns reports an attempt to compare runs produced by different
// specs or models, which is not a valid comparison.
var ErrMismatchedRuns = errors.New("runs have different spec or model and cannot be compared")
