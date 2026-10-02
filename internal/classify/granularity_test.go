package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func granularCatalog() taxonomy.Catalog {
	catalog := testCatalog()
	catalog.Version = "2026-10-02.1"
	catalog.Topics = append(catalog.Topics,
		taxonomy.Term{ID: "portrait_photography", Label: "写真", Active: true, Granularity: "specific", Description: "人物写真制作", Includes: []string{"人像写真制作"}, Excludes: []string{"仅照片配图"}, RecallTerms: []string{"写真", "portrait photo"}, Relations: []taxonomy.TermRelation{{ID: "llm", Kind: "related"}}},
		taxonomy.Term{ID: "whiteboard_animation", Label: "白板动画", Active: true, Granularity: "specific", Description: "白板动画制作", Includes: []string{"白板演示动画"}, Excludes: []string{"普通白板照片"}, RecallTerms: []string{"白板动画", "whiteboard animation"}})
	catalog.ResourceKinds = []taxonomy.Term{{ID: "skill", Label: "Skill", Active: true, Description: "可复用 Agent 技能"}}
	catalog.ContentFunctions = []taxonomy.Term{{ID: "method", Label: "方法", Active: true}}
	return catalog
}

func granularProvider(t *testing.T, calls *atomic.Int64, sent *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request providerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		ids := []string{}
		answers := map[string]any{}
		for id, question := range request.Questions {
			ids = append(ids, id)
			if question.Type == TypeNoul {
				answers[id] = map[string]any{"type": "noul", "noul": .9}
			} else {
				var choices map[string]any
				_ = json.Unmarshal(question.Criteria, &choices)
				probabilities := map[string]float64{}
				for choice := range choices {
					probabilities[choice] = 0
				}
				probabilities["none"] = 1
				answers[id] = map[string]any{"type": "choice", "choice": "none", "probabilities": probabilities}
			}
		}
		sort.Strings(ids)
		*sent = append(*sent, ids)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}})
	}))
}

func TestCandidateRecallExecutesOnlyRecalledQuestionsAndRestoresUnknownPartition(t *testing.T) {
	var calls atomic.Int64
	var sent [][]string
	server := granularProvider(t, &calls, &sent)
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), granularCatalog())
	if err != nil {
		t.Fatal(err)
	}
	client, err = client.WithCandidatePolicy(CandidatePolicy{Version: CandidatePolicyVersion, MaxQuestions: 9})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), Input{OriginalText: "这是一份写真制作 Skill。", Note: "白板动画"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || result.Coverage != "complete" || result.RawJudgments.MetadataVersion != 2 ||
		!slices.Contains(sent[0], "topic_portrait_photography") || slices.Contains(sent[0], "topic_whiteboard_animation") {
		t.Fatalf("candidate request or completeness is wrong: sent=%v raw=%+v", sent, result.RawJudgments)
	}
	if _, judged := result.RawJudgments.Judgments["topic_whiteboard_animation"]; judged {
		t.Fatal("private note or omitted candidate manufactured a judgment")
	}
	proposals, err := Decide(result.RawJudgments, result.Policy)
	if err != nil {
		t.Fatal(err)
	}
	omitted := decisionFor(proposals, "topics", "whiteboard_animation")
	encoded, _ := json.Marshal(omitted)
	if omitted.Verdict != VerdictAbstained || omitted.Reason != "not_recalled" || bytes.Contains(encoded, []byte("probability")) {
		t.Fatalf("omitted candidate became a negative probability: %s", encoded)
	}
	answers, _ := json.Marshal(result.Answers)
	metadata, _ := json.Marshal(result.RawJudgments)
	restored, err := RestoreStoredJudgments(client.Spec(), result.RequestedModel, result.Model, answers, result.Coverage, metadata)
	if err != nil || ValidateReplayMetadata(client.Spec(), restored) != nil {
		t.Fatalf("candidate replay lost immutable provenance: %v", err)
	}
	for _, mutation := range []string{"selection_hash", "omission", "max", "selected", "answer", "granularity"} {
		t.Run(mutation, func(t *testing.T) {
			var changed RawJudgments
			_ = json.Unmarshal(metadata, &changed)
			switch mutation {
			case "selection_hash":
				changed.CandidateManifest.SelectionHash = "bad"
			case "omission":
				changed.CandidateManifest.Omitted[0].Reason = "candidate_limit"
			case "max":
				changed.CandidateManifest.MaxQuestions--
			case "selected":
				changed.CandidateManifest.SelectedQuestionIDs = changed.CandidateManifest.SelectedQuestionIDs[1:]
			case "answer":
				delete(changed.Judgments, "topic_portrait_photography")
			case "granularity":
				j := changed.Judgments["topic_portrait_photography"]
				j.Granularity = ""
				changed.Judgments[j.QuestionID] = j
			}
			blob, _ := json.Marshal(changed)
			if _, err := RestoreStoredJudgments(client.Spec(), result.RequestedModel, result.Model, answers, result.Coverage, blob); err == nil {
				t.Fatal("forged candidate coverage accepted")
			}
		})
	}
	if path := os.Getenv("CAIRN_GRANULAR_FIXTURE_PATH"); path != "" {
		fixture, err := json.MarshalIndent(map[string]any{"spec": client.Spec(), "spec_hash": client.Spec().SemanticHash, "result": result}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Explicit local test artifact; it contains synthetic source only.
		if err := os.WriteFile(path, fixture, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCandidatePlanBoundsAndDeterministicContextWithoutMembershipInheritance(t *testing.T) {
	spec, err := CompileSpec(granularCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	policy := CandidatePolicy{Version: CandidatePolicyVersion, MaxQuestions: 9}
	for _, text := range []string{"ＰＯＲＴＲＡＩＴ　ＰＨＯＴＯ", "用portrait photo制作"} {
		plan, err := PlanCandidates(spec, Evidence{Primary: text, Coverage: "complete"}, policy)
		if err != nil || !slices.Contains(plan.SelectedQuestionIDs, "topic_portrait_photography") {
			t.Fatalf("bounded normalization dropped a valid phrase: %q %v", text, err)
		}
	}
	plan, err := PlanCandidates(spec, Evidence{Primary: "portrait photographer", Coverage: "complete"}, policy)
	if err != nil || slices.Contains(plan.SelectedQuestionIDs, "topic_portrait_photography") {
		t.Fatal("a partial English word recalled an unrelated topic")
	}
	evidence := Evidence{Primary: "写真与白板动画方法。", Coverage: "complete"}
	plan, err = PlanCandidates(spec, evidence, policy)
	if err != nil || len(plan.SelectedQuestionIDs) != 9 || plan.Omitted[0].Reason != "candidate_limit" || plan.Omitted[0].TermID != "whiteboard_animation" {
		t.Fatalf("candidate budget or stable ties changed: %+v %v", plan, err)
	}
	for range 20 {
		again, _ := PlanCandidates(spec, evidence, policy)
		if again.SelectionHash != plan.SelectionHash {
			t.Fatal("candidate identity depends on map iteration")
		}
	}
	if _, err := PlanCandidates(spec, evidence, CandidatePolicy{Version: CandidatePolicyVersion, MaxQuestions: 7}); err == nil {
		t.Fatal("candidate mode omitted core questions to fit its budget")
	}
}

func TestSpecificReservationsKeepBroadResourceAndOriginalProbability(t *testing.T) {
	specific := rawNoul("portrait_photography", .8)
	specific.Granularity = "specific"
	raw := completeRaw(specific, rawNoul("image_creation", .99), densityNoul("resource_kinds", "skill", .81),
		densityNoul("content_functions", "method", .98), densityNoul("content_functions", "case", .97),
		densityNoul("content_functions", "data", .96), rawNoul("video_creation", .95))
	got, err := Decide(raw, DefaultPolicy())
	if err != nil || !slices.Equal(got.Topics, []string{"portrait_photography", "image_creation"}) ||
		!slices.Equal(got.ResourceKinds, []string{"skill"}) || len(got.ContentFunctions) != 2 {
		t.Fatalf("supported specificity, broad subject or resource was discarded: %+v %v", got, err)
	}
	if decisionFor(got, "topics", "portrait_photography").Probability != .8 {
		t.Fatal("presentation preference changed a Noul probability")
	}
	legacy := DefaultPolicy()
	legacy.Version, legacy.PreferSpecificTopics = "jev-policy-v4", false
	old, err := Decide(raw, legacy)
	if err != nil || slices.Contains(old.Topics, "portrait_photography") {
		t.Fatal("historic v4 gained new reservation semantics")
	}
	legacy.PreferSpecificTopics = true
	if legacy.Validate() == nil {
		t.Fatal("new policy behavior reused historical identity")
	}
	for _, override := range []Override{
		{Field: "topics", Term: "portrait_photography", Action: OverrideReject, Revision: 1},
		{Field: "topics", Action: OverrideSetEmpty, Revision: 1},
	} {
		effective := Resolve(got, []Override{override})
		if slices.Contains(effective.Topics, "portrait_photography") {
			t.Fatal("specific reservation overrode a human rejection or clear")
		}
	}
	added := Resolve(got, []Override{{Field: "topics", Term: "manual_topic", Action: OverrideAccept, Revision: 1}})
	if !slices.Contains(added.Topics, "manual_topic") || len(added.Topics)+len(added.ResourceKinds)+len(added.ContentFunctions) != 6 {
		t.Fatal("automatic five-label cap deleted an explicit human addition")
	}
	specific.Noul = probability(.79)
	weak, _ := Decide(completeRaw(specific, rawNoul("image_creation", .99)), DefaultPolicy())
	if slices.Contains(weak.Topics, "portrait_photography") {
		t.Fatal("specific reservation lowered acceptance")
	}
}

func probability(p float64) *float64 { return &p }

func TestCatalogGrowthReusesCoreAnswersBeyondRequestLimit(t *testing.T) {
	var calls atomic.Int64
	var sent [][]string
	server := granularProvider(t, &calls, &sent)
	defer server.Close()
	catalog := testCatalog()
	catalog.Topics = nil
	for i := range 29 {
		catalog.Topics = append(catalog.Topics, taxonomy.Term{ID: fmt.Sprintf("core_%02d", i), Label: fmt.Sprintf("core_%02d", i), Active: true})
	}
	old, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{OriginalText: "客观证据正文。"}
	first, err := old.Classify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	first.RawJudgments.SourceRunID = 42
	for i := range 24 {
		catalog.Topics = append(catalog.Topics, taxonomy.Term{ID: fmt.Sprintf("specific_%02d", i), Label: fmt.Sprintf("specific_%02d", i), Active: true, Granularity: "specific", Description: "受控具体主题", Includes: []string{"实质讨论"}, Excludes: []string{"偶然提及"}, RecallTerms: []string{fmt.Sprintf("concept %d", i)}})
	}
	catalog.Version = "granular-new"
	next, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	result, err := next.ClassifyReusing(context.Background(), input, &first.RawJudgments, first.RawJudgments.BatchSemantics)
	if err != nil || calls.Load() != 2 || len(sent[1]) != 24 || len(result.RawJudgments.Reused) != 31 || result.Coverage != "complete" || result.RawJudgments.MetadataVersion != 1 {
		t.Fatalf("catalog growth paid again for unchanged core: calls=%d batches=%v raw=%+v err=%v", calls.Load(), sent, result.RawJudgments, err)
	}
	answers, _ := json.Marshal(result.Answers)
	metadata, _ := json.Marshal(result.RawJudgments)
	if _, err := RestoreStoredJudgments(next.Spec(), result.RequestedModel, result.Model, answers, result.Coverage, metadata); err != nil {
		t.Fatalf("full 55-question provenance was not replayable: %v", err)
	}
}
