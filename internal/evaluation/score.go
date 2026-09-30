package evaluation

import (
	"errors"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
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
	Unknown      int     `json:"unknown_reference"`
	Missing      int     `json:"missing_prediction"`
	Decided      int     `json:"decided"`
	CorrectEmpty int     `json:"correct_empty"`
	Precision    float64 `json:"precision"`
	Recall       float64 `json:"recall"`
	F1           float64 `json:"f1"`
}

// Report is the machine-readable evaluation result.
type Report struct {
	KnownReferenceSamples int                       `json:"known_reference_samples"`
	IndependentGroups     int                       `json:"independent_groups"`
	ReferenceHash         string                    `json:"reference_hash"`
	ReferenceProvenance   map[Provenance]int        `json:"reference_provenance"`
	KnownFields           int                       `json:"known_fields"`
	DecidedFields         int                       `json:"decided_fields"`
	PerLabel              []DimensionMetric         `json:"per_label"`
	Confusion             map[string]map[string]int `json:"confusion"`
	Intervals             map[string]Interval       `json:"intervals"`
	LabelMacroPrecision   float64                   `json:"label_macro_precision"`
	LabelMacroRecall      float64                   `json:"label_macro_recall"`
	DatasetHash           string                    `json:"dataset_hash"`
	DatasetName           string                    `json:"dataset_name"`
	Split                 string                    `json:"split"`
	SamplesTotal          int                       `json:"samples_total"`
	SamplesWithGold       int                       `json:"samples_with_gold"`
	SamplesMissing        int                       `json:"samples_missing_gold"`
	Dimensions            []DimensionMetric         `json:"dimensions"`
	MicroPrecision        float64                   `json:"micro_precision"`
	MicroRecall           float64                   `json:"micro_recall"`
	MacroPrecision        float64                   `json:"macro_precision"`
	MacroRecall           float64                   `json:"macro_recall"`
	AcceptedError         float64                   `json:"accepted_error_rate"`
	Coverage              float64                   `json:"coverage"`
	ReviewFields          float64                   `json:"review_fields_per_sample"`
	Brier                 float64                   `json:"brier,omitempty"`
	ECE                   float64                   `json:"ece,omitempty"`
	CalibrationBins       int                       `json:"calibration_bins,omitempty"`
	HasCalibration        bool                      `json:"has_calibration"`
	Inconclusive          bool                      `json:"inconclusive"`
	InconclusiveWhy       []string                  `json:"inconclusive_reasons,omitempty"`
	ByLanguage            map[string]int            `json:"by_language"`
	ByLengthBucket        map[string]int            `json:"by_length_bucket"`
	ByCarrier             map[string]int            `json:"by_carrier"`
	MissingGoldCount      int                       `json:"missing_gold_count"`
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
	referenceHash, err := HashReference(dataset)
	if err != nil {
		return Report{}, err
	}
	report := Report{DatasetHash: hash, ReferenceHash: referenceHash, DatasetName: dataset.Name, Split: dataset.Split, SamplesTotal: len(dataset.Samples),
		ByLanguage: map[string]int{}, ByLengthBucket: map[string]int{}, ByCarrier: map[string]int{}, ReferenceProvenance: map[Provenance]int{}, Confusion: map[string]map[string]int{}}
	order := []string{"topics", "content_functions", "carriers", "affordances", "form", "use"}
	dimensions, labels := map[string]*DimensionMetric{}, map[string]*DimensionMetric{}
	for _, name := range order {
		dimensions[name] = &DimensionMetric{Dimension: name, MultiLabel: name == "topics" || name == "content_functions" || name == "affordances"}
	}
	var reviewFields, brierCount int
	var brierSum float64
	var calibration []calibPoint
	groups := map[string]counts{}
	for _, sample := range dataset.Samples {
		report.ByLanguage[NormalizeLanguage(sample.Language)]++
		report.ByLengthBucket[sample.LengthBucket]++
		report.ByCarrier[sample.Carrier]++
		report.ReferenceProvenance[sample.Provenance]++
		if sample.Gold == nil {
			report.SamplesMissing++
			continue
		}
		report.SamplesWithGold++
		prediction, present := predictions[sample.SampleID]
		if !present {
			report.MissingGoldCount++
		}
		beforeKnown := report.KnownFields
		references := referenceLabels(*sample.Gold)
		predicted := map[string][]string{"topics": prediction.Topics, "content_functions": prediction.ContentFunctions, "carriers": prediction.Carriers, "affordances": prediction.Affordances, "form": nonEmpty(prediction.Form), "use": nonEmpty(prediction.Use)}
		group := sample.GroupID
		if group == "" {
			group = sample.SampleID
		}
		values := groups[group]
		for _, name := range order {
			label := references[name]
			metric := dimensions[name]
			if !knownLabel(label) {
				metric.Unknown++
				continue
			}
			report.KnownFields++
			values.known++
			actual := predicted[name]
			abstained := dimensionAbstained(prediction, name)
			if !present || abstained {
				reviewFields++
			} else {
				report.DecidedFields++
				metric.Decided++
				values.decided++
			}
			previousTP, previousFP, previousFN := metric.TruePositive, metric.FalsePos, metric.FalseNeg
			evaluateReference(metric, label, actual, abstained || !present)
			if !present {
				metric.Abstained--
				metric.Missing++
			}
			values.tp += metric.TruePositive - previousTP
			values.fp += metric.FalsePos - previousFP
			values.fn += metric.FalseNeg - previousFN
			// Single-choice acceptable alternatives are one correct outcome, not
			// several required positives. Ambiguous alternatives are excluded from
			// per-label confusion while remaining scored at the dimension level.
			if !metric.MultiLabel {
				if report.Confusion[name] == nil {
					report.Confusion[name] = map[string]int{}
				}
				expected := append([]string{}, label.Values...)
				sort.Strings(expected)
				key := strings.Join(expected, "|") + " -> " + strings.Join(actual, "|")
				if !present {
					key = strings.Join(expected, "|") + " -> <missing>"
				} else if abstained {
					key = strings.Join(expected, "|") + " -> <abstained>"
				}
				report.Confusion[name][key]++
				if len(label.Values) > 1 {
					continue
				}
			}
			terms := map[string]bool{}
			for _, term := range append(append([]string{}, label.Values...), actual...) {
				terms[term] = true
			}
			for term := range terms {
				key := name + ":" + term
				item := labels[key]
				if item == nil {
					item = &DimensionMetric{Dimension: key, MultiLabel: metric.MultiLabel}
					labels[key] = item
				}
				item.Support++
				expected, found := contains(label.Values, term), contains(actual, term)
				if expected && found {
					item.TruePositive++
				} else if found {
					item.FalsePos++
				} else if expected {
					item.FalseNeg++
				}
			}
		}
		groups[group] = values
		if report.KnownFields > beforeKnown {
			report.KnownReferenceSamples++
		}
		if present && knownLabel(sample.Gold.Topics) {
			keys := make([]string, 0, len(prediction.TopicProbabilities))
			for label := range prediction.TopicProbabilities {
				keys = append(keys, label)
			}
			sort.Strings(keys)
			for _, label := range keys {
				probability := prediction.TopicProbabilities[label]
				correct := contains(sample.Gold.Topics.Values, label)
				brierSum += math.Pow(probability-boolToFloat(correct), 2)
				brierCount++
				calibration = append(calibration, calibPoint{p: probability, correct: correct})
			}
		}
	}
	var tp, fp, fn, scored int
	for _, name := range order {
		metric := dimensions[name]
		finalize(metric)
		report.Dimensions = append(report.Dimensions, *metric)
		tp += metric.TruePositive
		fp += metric.FalsePos
		fn += metric.FalseNeg
		if metric.TruePositive+metric.FalsePos+metric.FalseNeg > 0 {
			report.MacroPrecision += metric.Precision
			report.MacroRecall += metric.Recall
			scored++
		}
	}
	if scored > 0 {
		report.MacroPrecision /= float64(scored)
		report.MacroRecall /= float64(scored)
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		metric := labels[key]
		finalize(metric)
		report.PerLabel = append(report.PerLabel, *metric)
		report.LabelMacroPrecision += metric.Precision
		report.LabelMacroRecall += metric.Recall
	}
	if len(keys) > 0 {
		report.LabelMacroPrecision /= float64(len(keys))
		report.LabelMacroRecall /= float64(len(keys))
	}
	report.MicroPrecision = ratio(tp, tp+fp)
	report.MicroRecall = ratio(tp, tp+fn)
	report.AcceptedError = ratio(fp, tp+fp)
	report.Coverage = ratio(report.DecidedFields, report.KnownFields)
	if report.SamplesWithGold > 0 {
		report.ReviewFields = float64(reviewFields) / float64(report.SamplesWithGold)
	}
	if brierCount > 0 {
		report.HasCalibration = true
		report.Brier = brierSum / float64(brierCount)
		report.ECE, report.CalibrationBins = expectedCalibrationError(calibration, 10)
	}
	for _, count := range groups {
		if count.known > 0 {
			report.IndependentGroups++
		}
	}
	if report.KnownReferenceSamples < minSupportForConclusion {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "insufficient reference samples")
	}
	if report.SamplesMissing > 0 {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "missing reference for some samples")
	}
	if report.MissingGoldCount > 0 {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "reference without a prediction")
	}
	if report.KnownFields == 0 {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "no known reference fields")
	}
	if report.IndependentGroups < minSupportForConclusion {
		report.InconclusiveWhy = append(report.InconclusiveWhy, "insufficient independent reference groups")
	}
	sort.Strings(report.InconclusiveWhy)
	report.Inconclusive = len(report.InconclusiveWhy) > 0
	report.Intervals = bootstrap(groups)
	return report, nil
}

// Interval is a deterministic 95% group-bootstrap percentile interval. Groups,
// not labels or translated variants, are resampled together. It describes the
// declared benchmark; it cannot quantify reference-label bias.
type Interval struct {
	Low        float64 `json:"low"`
	High       float64 `json:"high"`
	Groups     int     `json:"groups"`
	Replicates int     `json:"replicates"`
	Method     string  `json:"method"`
}
type counts struct{ tp, fp, fn, known, decided int }

func bootstrap(groups map[string]counts) map[string]Interval {
	keys := make([]string, 0, len(groups))
	for key, c := range groups {
		if c.known > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) < 2 {
		return nil
	}
	const replicates = 500
	metrics := map[string][]float64{"micro_precision": {}, "micro_recall": {}, "accepted_error_rate": {}, "coverage": {}}
	// #nosec G404 -- fixed-seed statistical resampling, never a security token.
	rng := rand.New(rand.NewPCG(22, 202609))
	for range replicates {
		var sum counts
		for range len(keys) {
			value := groups[keys[rng.IntN(len(keys))]]
			sum.tp += value.tp
			sum.fp += value.fp
			sum.fn += value.fn
			sum.known += value.known
			sum.decided += value.decided
		}
		metrics["micro_precision"] = append(metrics["micro_precision"], ratio(sum.tp, sum.tp+sum.fp))
		metrics["micro_recall"] = append(metrics["micro_recall"], ratio(sum.tp, sum.tp+sum.fn))
		metrics["accepted_error_rate"] = append(metrics["accepted_error_rate"], ratio(sum.fp, sum.tp+sum.fp))
		metrics["coverage"] = append(metrics["coverage"], ratio(sum.decided, sum.known))
	}
	out := map[string]Interval{}
	for name, values := range metrics {
		sort.Float64s(values)
		out[name] = Interval{Low: values[12], High: values[487], Groups: len(keys), Replicates: replicates, Method: "group_bootstrap_95_percentile_seed_22_202609"}
	}
	return out
}

func evaluateReference(metric *DimensionMetric, label Label, predicted []string, abstained bool) {
	metric.Support++
	if abstained {
		metric.Abstained++
	}
	if len(label.Values) == 0 {
		metric.FalsePos += len(predicted)
		if len(predicted) == 0 && !abstained {
			metric.CorrectEmpty++
		}
		return
	}
	if !metric.MultiLabel {
		if len(predicted) > 0 && contains(label.Values, predicted[0]) {
			metric.TruePositive++
			return
		}
		metric.FalseNeg++
		if len(predicted) > 0 {
			metric.FalsePos++
		}
		return
	}
	for _, value := range predicted {
		if contains(label.Values, value) {
			metric.TruePositive++
		} else {
			metric.FalsePos++
		}
	}
	for _, value := range label.Values {
		if !contains(predicted, value) {
			metric.FalseNeg++
		}
	}
}

// FormMatches reports whether a predicted single value is in the acceptable set.
func (p Prediction) FormMatches(label Label) bool {
	if label.Unknown || label.NotApplicable {
		return p.Form == ""
	}
	return contains(label.Values, p.Form)
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

func dimensionAbstained(prediction Prediction, dimension string) bool {
	aliases := map[string]string{"topics": "topic", "content_functions": "content_function", "carriers": "carrier", "affordances": "affordance"}
	return contains(prediction.Abstained, dimension) || (aliases[dimension] != "" && contains(prediction.Abstained, aliases[dimension]))
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
