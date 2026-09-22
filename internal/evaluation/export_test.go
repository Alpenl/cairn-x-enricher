package evaluation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type fakeRunSource struct {
	runs     []cairn.StoredRun
	spec     cairn.StoredQuestionSpec
	evidence json.RawMessage
}

func (f fakeRunSource) GetRuns(context.Context, int64) ([]cairn.StoredRun, error) {
	return f.runs, nil
}

func (f fakeRunSource) GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error) {
	return f.spec, nil
}

func (f fakeRunSource) GetEvidence(context.Context, int64) (json.RawMessage, error) {
	return f.evidence, nil
}

func exportCatalog() taxonomy.Catalog {
	return taxonomy.Catalog{
		Version: "2026-09-20.1",
		Topics: []taxonomy.Term{
			{ID: "llm", Label: "LLM", Description: "大语言模型", Active: true},
			{ID: "eval", Label: "评估", Description: "评估", Active: true},
		},
		Forms:            []taxonomy.Term{{ID: "method", Label: "方法", Description: "方法", Active: true}},
		Uses:             []taxonomy.Term{{ID: "try", Label: "待试", Description: "待试", Active: true}},
		ContentFunctions: []taxonomy.Term{{ID: "method", Label: "方法", Description: "方法", Active: true}},
		Carriers:         []taxonomy.Term{{ID: "single", Label: "单帖", Description: "单帖", Active: true}},
		Affordances:      []taxonomy.Term{{ID: "practice", Label: "可实践", Description: "可实践", Active: true}},
	}
}

func exportFixture(t *testing.T) fakeRunSource {
	t.Helper()
	spec, err := classify.CompileSpec(exportCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := classify.MarshalSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	probability := 0.9
	answers := map[string]classify.RawAnswer{}
	for _, question := range spec.Questions {
		switch question.Kind {
		case classify.QuestionNoul:
			answers[question.ID] = classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &probability}}
		case classify.QuestionChoice:
			options := question.AnswerOptions()
			distribution := map[string]float64{}
			for index, option := range options {
				if index == 0 {
					distribution[option] = 1
				} else {
					distribution[option] = 0
				}
			}
			answers[question.ID] = classify.RawAnswer{Type: classify.TypeChoice,
				Choice: &classify.ChoiceAnswer{Choice: options[0], Probabilities: distribution}}
		}
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(classify.DefaultPolicy())
	return fakeRunSource{
		runs: []cairn.StoredRun{{
			ID: 5, ContentRevision: 2, SpecID: spec.SpecID, SpecHash: spec.SemanticHash,
			RequestedModel: "jev-latest", ResolvedModel: "jev-1.13.0", PolicyVersion: "jev-policy-v2",
			Policy: policy, Answers: encoded, Coverage: "complete", Status: "succeeded",
		}},
		spec:     cairn.StoredQuestionSpec{SpecID: spec.SpecID, SpecHash: spec.SemanticHash, Payload: payload},
		evidence: json.RawMessage(`{"content_hash":"evidence-hash"}`),
	}
}

// TestExportDatasetUsesTheRealRunIdentity proves the exported prediction is the
// deterministic decision over the stored answers, carries the real spec/model/
// policy identity, and never fabricates gold.
func TestExportDatasetUsesTheRealRunIdentity(t *testing.T) {
	source := exportFixture(t)
	dataset, err := ExportDataset(context.Background(), source, ExportOptions{LinkIDs: []int64{7}, Name: "prod", Split: "holdout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Samples) != 1 || len(dataset.Prediction) != 1 {
		t.Fatalf("dataset = %+v", dataset)
	}
	sample := dataset.Samples[0]
	if sample.SourceHash != "evidence-hash" {
		t.Fatalf("source hash = %q, want the stored evidence hash", sample.SourceHash)
	}
	if sample.Provenance != ProvenanceSynthetic || sample.Gold != nil {
		t.Fatalf("a machine prediction must not carry gold: %+v", sample)
	}
	prediction := dataset.Prediction[0]
	if prediction.SpecID == "" || prediction.Model != "jev-1.13.0" || prediction.PolicyVersion != "jev-policy-v2" {
		t.Fatalf("prediction lost its identity: %+v", prediction)
	}
	if len(prediction.Topics) == 0 || prediction.TopicProbabilities["llm"] != 0.9 {
		t.Fatalf("prediction lost its topics or distribution: %+v", prediction)
	}
	// The scorer must refuse to certify quality without gold.
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.SamplesWithGold != 0 || !report.Inconclusive {
		t.Fatalf("a gold-free export must score as inconclusive: %+v", report)
	}
}

// TestExportDatasetRejectsUnboundedOrUnreplayableInput covers the safety edges.
func TestExportDatasetRejectsUnboundedOrUnreplayableInput(t *testing.T) {
	source := exportFixture(t)
	if _, err := ExportDataset(context.Background(), source, ExportOptions{}); err == nil {
		t.Fatal("an export without explicit ids must be refused")
	}
	tooMany := make([]int64, 0, 501)
	for index := 0; index < 501; index++ {
		tooMany = append(tooMany, int64(index+1))
	}
	if _, err := ExportDataset(context.Background(), source, ExportOptions{LinkIDs: tooMany}); err == nil {
		t.Fatal("an unbounded export must be refused")
	}
	// A run without a recoverable historical policy is not exportable.
	broken := exportFixture(t)
	broken.runs[0].Policy = nil
	if _, err := ExportDataset(context.Background(), broken, ExportOptions{LinkIDs: []int64{7}}); err == nil {
		t.Fatal("a run without a historical policy must not be exported")
	}
	// A partial/failed trailing run is skipped, not exported as current.
	partial := exportFixture(t)
	partial.runs = append(partial.runs, cairn.StoredRun{ID: 6, Status: "partial", Coverage: "partial", Answers: json.RawMessage(`{}`)})
	dataset, err := ExportDataset(context.Background(), partial, ExportOptions{LinkIDs: []int64{7}})
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Prediction[0].SampleID != "link-7-run-5" {
		t.Fatalf("a partial trailing run must be skipped: %+v", dataset.Prediction[0])
	}
}

func TestTopicCalibrationExcludesOtherNoulDimensions(t *testing.T) {
	value := 0.9
	raw := classify.RawJudgments{Judgments: map[string]classify.RawJudgment{
		"topic_llm":           {Kind: classify.QuestionNoul, Dimension: "topic", TermID: "llm", Noul: &value},
		"function_method":     {Kind: classify.QuestionNoul, Dimension: "content_functions", TermID: "method", Noul: &value},
		"affordance_practice": {Kind: classify.QuestionNoul, Dimension: "affordances", TermID: "practice", Noul: &value},
	}}
	probabilities := topicProbabilities(raw)
	if len(probabilities) != 1 || probabilities["llm"] != 0.9 {
		t.Fatalf("non-topic probabilities contaminated calibration: %v", probabilities)
	}
}
