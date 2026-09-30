package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func personalTagCatalog() taxonomy.Catalog {
	catalog := testCatalog()
	catalog.Version = "2026-09-30.1"
	catalog.DefinitionVersion = 3
	catalog.Topics = nil
	for _, id := range []string{"ai_coding", "agent_workflow", "image_creation", "video_creation", "writing_creation", "ui_design", "knowledge_workflow", "information_sources", "model_practice", "creator_business", "finance_resources", "document_layout"} {
		catalog.Topics = append(catalog.Topics, taxonomy.Term{ID: id, Label: id, Active: true, Status: "active", DefinitionVersion: 1, DisplayRevision: 1, Description: id})
	}
	catalog.Topics = append(catalog.Topics, taxonomy.Term{ID: "llm", Label: "LLM", Active: false, Status: "deprecated", DefinitionVersion: 1, DisplayRevision: 1})
	for _, id := range []string{"skill", "prompt", "software", "component", "model", "reference"} {
		catalog.ResourceKinds = append(catalog.ResourceKinds, taxonomy.Term{ID: id, Label: id, Active: true, Status: "active", DefinitionVersion: 1, DisplayRevision: 1, Description: "明确介绍或提供可复用的" + id})
	}
	catalog.ResourceKinds[0].Includes = []string{"可安装的 Agent 技能包"}
	catalog.ResourceKinds[0].Excludes = []string{"泛泛讨论 SKILL.md 配置文件"}
	for _, id := range []string{"method", "tool", "case", "data", "opinion"} {
		catalog.ContentFunctions = append(catalog.ContentFunctions, taxonomy.Term{ID: id, Label: id, Active: true})
	}
	for _, id := range []string{"quote", "practice", "background", "material"} {
		catalog.Affordances = append(catalog.Affordances, taxonomy.Term{ID: id, Label: id, Active: true})
	}
	catalog.Carriers = []taxonomy.Term{{ID: "single", Label: "single", Active: true}}
	return catalog
}

func TestPersonalTagsCompileIndependentResourcesWithinOneRequest(t *testing.T) {
	spec, err := CompileSpec(personalTagCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	core := 0
	for _, question := range spec.Questions {
		if question.ID == "topic_llm" {
			t.Fatal("retired topic was evaluated as a current candidate")
		}
		if question.Dimension == "topic" || question.Dimension == "resource_kinds" {
			core++
			if question.Kind != QuestionNoul || len(question.DependsOn) != 0 {
				t.Fatal("multi-label core became a competing or dependent question")
			}
		}
		if question.ID == "resource_kind_skill" && (!strings.Contains(string(question.Instructions), "可安装的 Agent 技能包") || !strings.Contains(string(question.Instructions), "SKILL.md 配置文件")) {
			t.Fatal("resource boundary was lost from the model-facing question")
		}
	}
	if core != 18 || len(spec.Questions) != 30 {
		t.Fatalf("core=%d all=%d; expected 18 core plus 12 compatibility questions", core, len(spec.Questions))
	}
	chunks, err := PlanChunks(spec, DefaultMaxQuestionsPerRequest)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("first tag release unexpectedly needs more than one provider call: chunks=%d err=%v", len(chunks), err)
	}
}

func TestResourceDefinitionIdentitySeparatesDisplayFromMeaning(t *testing.T) {
	catalog := personalTagCatalog()
	before, _ := CompileSpec(catalog, false)
	catalog.ResourceKinds[0].Label = "我的技能包"
	catalog.ResourceKinds[0].DisplayRevision++
	afterRename, _ := CompileSpec(catalog, false)
	if before.SemanticHash != afterRename.SemanticHash {
		t.Fatal("display rename invalidated inference identity")
	}
	catalog.ResourceKinds[0].Description = "只表示一套新含义的技能包"
	catalog.ResourceKinds[0].DefinitionVersion++
	afterDefinition, _ := CompileSpec(catalog, false)
	if before.SpecID == afterDefinition.SpecID || before.SemanticHash == afterDefinition.SemanticHash {
		t.Fatal("meaning change reused an immutable question identity")
	}
}

func TestResourceTagsPersistReplayAndKeepIncrementalHumanDecisions(t *testing.T) {
	catalog := personalTagCatalog()
	spec, _ := CompileSpec(catalog, false)
	var answers map[string]RawAnswer
	if err := json.Unmarshal(completeAnswersFor(t, spec), &answers); err != nil {
		t.Fatal(err)
	}
	for id, answer := range answers {
		if answer.Type == TypeNoul {
			answer.Noul.Noul = ptr(0.05)
			answers[id] = answer
		}
	}
	answers["topic_image_creation"] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: ptr(0.97)}}
	answers["resource_kind_skill"] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: ptr(0.96)}}
	answers["resource_kind_software"] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: ptr(0.5)}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request providerRequestShape
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Questions["resource_kind_skill"].Type != "noul" || len(request.Questions) != 30 {
			t.Error("resource questions did not reach the official wire contract")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 5000, "output_tokens": 400}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), Input{OriginalText: "安装这个写真 Skill 来生成图像。", Note: "我想写代码"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Automatic.Topics, []string{"image_creation"}) || !slices.Equal(result.Automatic.ResourceKinds, []string{"skill"}) {
		t.Fatalf("core proposals were lost or uncertain resource was forced: %+v", result.Automatic)
	}
	encoded, _ := json.Marshal(result.RawJudgments)
	var stored RawJudgments
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	newPolicy := result.Policy
	newPolicy.Version += "+resource-evaluation"
	newPolicy.TopicAccept = 0.45
	_, after, changed, err := Replay(stored, result.Policy, newPolicy)
	if err != nil || !slices.Contains(changed, "resource_kinds") || !slices.Contains(after.ResourceKinds, "software") || calls != 1 {
		t.Fatalf("resource policy replay lost raw probability or called model: changed=%v calls=%d err=%v", changed, calls, err)
	}
	view := Resolve(after, []Override{
		{Field: "resource_kinds", Term: "skill", Action: OverrideReject, Revision: 1},
		{Field: "topics", Term: "video_creation", Action: OverrideAccept, Revision: 2},
	})
	if !slices.Equal(view.ResourceKinds, []string{"software"}) || view.Empty.ResourceKinds || !slices.Contains(view.Topics, "video_creation") {
		t.Fatalf("single-tag rejection froze new resources or replay overwrote human topic: %+v", view)
	}
	after.ResourceKinds = []string{}
	view = Resolve(after, []Override{{Field: "resource_kinds", Term: "skill", Action: OverrideAccept, Revision: 1}})
	if !slices.Equal(view.ResourceKinds, []string{"skill"}) {
		t.Fatal("model removed an explicitly adopted system resource")
	}
}

func TestResourceGroupEmptyAndPerTagResetRemainIndependent(t *testing.T) {
	proposals := Proposals{Topics: []string{"image_creation"}, ResourceKinds: []string{"skill", "software"}}
	view := Resolve(proposals, []Override{
		{Field: "resource_kinds", Action: OverrideSetEmpty, Revision: 1},
		{Field: "resource_kind", Term: "skill", Action: OverrideReset, Revision: 2},
	})
	if !slices.Equal(view.ResourceKinds, []string{"skill"}) || !slices.Equal(view.Topics, proposals.Topics) {
		t.Fatalf("per-tag readmission re-enabled the group or affected topics: %+v", view)
	}
}
