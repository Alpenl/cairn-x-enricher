package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func TestHistoricalScopeLeavesNewBroadAndSpecificTopicsUnknown(t *testing.T) {
	gold := &Gold{Topics: Label{Values: []string{"llm"}}, Form: Label{Unknown: true}, Use: Label{Unknown: true}}
	predicted := prediction("a", []string{"llm", "new_broad", "portrait_photography"}, "", "", map[string]float64{"llm": .9, "new_broad": .99, "portrait_photography": .99})
	report, err := Score(Dataset{Name: "historical-scope", Samples: []Sample{sample("a", gold)}, Prediction: []Prediction{predicted}})
	if err != nil {
		t.Fatal(err)
	}
	topics := metricFor(t, report, "topics")
	if topics.TruePositive != 1 || topics.FalsePos != 0 || report.Brier > .010000000000001 {
		t.Fatalf("new unreviewed concepts became negative gold: %+v", report)
	}
	gold.Topics.ReviewedTerms = []string{"new_broad", "portrait_photography"}
	reviewed, err := Score(Dataset{Name: "explicit-scope", Samples: []Sample{sample("a", gold)}, Prediction: []Prediction{predicted}})
	if err != nil || metricFor(t, reviewed, "topics").FalsePos != 2 {
		t.Fatalf("explicit reviewed negatives were ignored: %+v %v", reviewed, err)
	}
	gold.Topics = Label{Values: []string{"portrait_photography"}, ReviewedTerms: []string{}}
	encoded, _ := json.Marshal(gold)
	if !bytes.Contains(encoded, []byte(`"reviewed_terms":[]`)) {
		t.Fatal("positive-only scope was lost on export")
	}
	var decoded Gold
	_ = json.Unmarshal(encoded, &decoded)
	if reviewedTerm(decoded.Topics, "topics", "llm") || !reviewedTerm(decoded.Topics, "topics", "portrait_photography") {
		t.Fatal("positive-only scope inherited the legacy negative set")
	}
}

func TestCandidateRecallReportSeparatesOmissionsFromModelErrors(t *testing.T) {
	positive := .9
	raw := &classify.RawJudgments{CandidateManifest: &classify.CandidateManifest{
		SelectedQuestionIDs: []string{"topic_llm"},
		Omitted: []classify.CandidateOmission{
			{QuestionID: "topic_portrait_photography", Dimension: "topic", TermID: "portrait_photography", Reason: "not_recalled"},
			{QuestionID: "topic_whiteboard_animation", Dimension: "topic", TermID: "whiteboard_animation", Reason: "candidate_limit"},
		},
	}, Judgments: map[string]classify.RawJudgment{"topic_llm": {QuestionID: "topic_llm", Dimension: "topic", TermID: "llm", Noul: &positive}}}
	predicted := prediction("a", []string{"llm"}, "", "", nil)
	predicted.Evaluation = raw
	gold := &Gold{Topics: Label{Values: []string{"llm", "portrait_photography", "whiteboard_animation"}}}
	report, err := Score(Dataset{Name: "recall-loss", Samples: []Sample{sample("a", gold)}, Prediction: []Prediction{predicted}})
	if err != nil || report.CandidateRecall == nil {
		t.Fatal(err)
	}
	m := report.CandidateRecall
	if m.PositiveTopics != 3 || m.Recalled != 1 || m.NotRecalled != 1 || m.Limited != 1 || m.Recall != 1.0/3 {
		t.Fatalf("retrieval misses were silently scored as model negatives: %+v", m)
	}
	if metricFor(t, report, "topics").FalseNeg != 2 {
		t.Fatal("end-to-end positive recall hid omitted true labels")
	}
}

func TestCalibrationNewConceptNegativesNeedExplicitReview(t *testing.T) {
	dataset, _ := policyDataset(t)
	for _, term := range []string{"new_broad", "portrait_photography"} {
		if reviewedTerm(dataset.Samples[0].Gold.Topics, "topics", term) {
			t.Fatal("new negative has no historical annotation basis")
		}
	}
	// Exercise the production calibration accumulator with a newly named
	// provider question, rebinding the already validated synthetic evaluation
	// snapshot. No model or network is involved in this scope test.
	for i := range dataset.Prediction {
		raw := dataset.Prediction[i].Evaluation
		p := .9
		raw.Judgments["topic_new_broad"] = classify.RawJudgment{QuestionID: "topic_new_broad", Kind: classify.QuestionNoul, Dimension: "topic", TermID: "new_broad", Noul: &p}
		raw.Judgments["topic_portrait_photography"] = classify.RawJudgment{QuestionID: "topic_portrait_photography", Kind: classify.QuestionNoul, Dimension: "topic", TermID: "portrait_photography", Granularity: "specific", Noul: &p}
	}
	verified := &VerifiedReplay{dataset: dataset}
	report, err := verified.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	metric := probabilityMetric(t, report, "topics")
	if metric.UnknownReference != 4 || metric.Observations != 4 {
		t.Fatalf("unreviewed broad/specific p was calibrated as false: %+v", metric)
	}
	for i := range dataset.Samples {
		dataset.Samples[i].Gold.Topics.ReviewedTerms = []string{"llm", "eval", "new_broad", "portrait_photography"}
	}
	report, err = verified.Calibration(calibrationConfig())
	if err != nil || probabilityMetric(t, report, "topics").UnknownReference != 0 {
		t.Fatalf("explicit negatives did not enter calibration: %v", err)
	}
}

func TestSelectedCandidateMissingProviderAnswerStillCountsAsRecalled(t *testing.T) {
	gold := &Gold{Topics: Label{Values: []string{"portrait_photography"}}}
	predicted := prediction("a", nil, "", "", nil)
	predicted.Evaluation = &classify.RawJudgments{
		Coverage: "partial", Judgments: map[string]classify.RawJudgment{},
		CandidateManifest: &classify.CandidateManifest{SelectedQuestionIDs: []string{"topic_portrait_photography"}},
	}
	report, err := Score(Dataset{Name: "provider-missing", Samples: []Sample{sample("a", gold)}, Prediction: []Prediction{predicted}})
	if err != nil || report.CandidateRecall == nil {
		t.Fatal(err)
	}
	m := report.CandidateRecall
	if m.PositiveTopics != 1 || m.Recalled != 1 || m.SelectedUnanswered != 1 || m.NotRecalled != 0 || m.Limited != 0 || m.Recall != 1 {
		t.Fatalf("missing provider answer was attributed to retrieval: %+v", m)
	}
	if metricFor(t, report, "topics").FalseNeg != 1 {
		t.Fatal("selected candidate hid a missing end-to-end prediction")
	}
}

func TestPrivatePortraitConstraintIsOnlyOnePositiveReference(t *testing.T) {
	path := os.Getenv("CAIRN_PORTRAIT_CONSTRAINT_PATH")
	if path == "" {
		t.Skip("private reference fixture is opt-in")
	}
	//nolint:gosec // Explicit operator-selected private reference; opt-in local test only.
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dataset Dataset
	if err := json.Unmarshal(encoded, &dataset); err != nil {
		t.Fatal(err)
	}
	if err := dataset.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(dataset.Samples) != 1 || dataset.Samples[0].Provenance != ProvenanceHumanSingle || dataset.Samples[0].Gold == nil {
		t.Fatal("private fixture claims more than the user's one constraint")
	}
	s := dataset.Samples[0]
	dataset.Prediction = []Prediction{{SampleID: s.SampleID, Topics: []string{"portrait_photography", "image_creation", "new_broad"}}}
	report, err := Score(dataset)
	if err != nil || report.KnownFields != 1 || !report.Inconclusive || metricFor(t, report, "topics").FalsePos != 0 || metricFor(t, report, "topics").TruePositive != 1 {
		t.Fatalf("one positive constrained unrelated unknown labels or claimed completion: %+v %v", report, err)
	}
}
