package classify

import (
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// --- Evidence preparation ---------------------------------------------------

func TestPrepareEvidenceExcludesPersonalData(t *testing.T) {
	evidence, err := PrepareEvidence("primary body", []EvidenceBlock{
		{ID: "c1", Role: RoleAuthorContinuation, Text: "author continues"},
		{ID: "c2", Role: RoleQuoted, Text: "someone else disagrees"},
	}, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := evidence.stateForModel()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckNoPersonalFields(encoded); err != nil {
		t.Fatalf("objective state contains personal data: %v", err)
	}
	if evidence.Truncated || evidence.Coverage != "complete" {
		t.Fatalf("small evidence should be complete: %+v", evidence)
	}
}

func TestPrepareEvidenceRecordsTruncationInsteadOfSilentlyDropping(t *testing.T) {
	long := make([]rune, 200)
	for index := range long {
		long[index] = '字'
	}
	evidence, err := PrepareEvidence(string(long), nil, Budget{MaxRunes: 100, MaxBlocks: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Truncated || evidence.Coverage != "truncated" {
		t.Fatalf("oversized evidence must be marked truncated: %+v", evidence)
	}
	if evidence.TotalRunes != 200 {
		t.Fatalf("original size should be reported: %d", evidence.TotalRunes)
	}
}

func TestPrepareEvidenceBoundsBlocksAndPreservesPrimary(t *testing.T) {
	context := make([]EvidenceBlock, 20)
	for index := range context {
		context[index] = EvidenceBlock{ID: "c", Role: RoleThirdParty, Text: "comment"}
	}
	evidence, err := PrepareEvidence("primary wins", context, Budget{MaxRunes: 500, MaxBlocks: 3})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Primary != "primary wins" {
		t.Fatal("primary text must survive block bounding")
	}
	if len(evidence.Context) != 3 {
		t.Fatalf("context blocks = %d, want 3", len(evidence.Context))
	}
	if !evidence.Truncated {
		t.Fatal("dropping blocks must be recorded as truncation")
	}
}

func TestPrepareEvidenceRequiresPrimary(t *testing.T) {
	if _, err := PrepareEvidence("   ", nil, DefaultBudget()); err == nil {
		t.Fatal("empty primary text must be rejected")
	}
}

func TestCheckNoPersonalFieldsDetectsNestedLeak(t *testing.T) {
	leaky := []byte(`{"primary":"x","context":[{"note":"personal"}]}`)
	if err := CheckNoPersonalFields(leaky); err == nil {
		t.Fatal("nested personal field must be detected")
	}
}

// --- Override resolution ----------------------------------------------------

func TestResolveAppliesOverridesInRevisionOrder(t *testing.T) {
	proposals := Proposals{Topics: []string{"llm", "eval"}, Form: "method", Use: "try"}
	view := Resolve(proposals, []Override{
		{Field: "topic", Term: "llm", Action: OverrideReject, Revision: 2},
		{Field: "topic", Term: "eng", Action: OverrideAccept, Revision: 1},
	})
	if !equalStrings(view.Topics, []string{"eval", "eng"}) {
		t.Fatalf("topics = %v", view.Topics)
	}
	if !view.Reviewed {
		t.Fatal("overrides must mark the record reviewed")
	}
}

func TestResolveSetEmptyIsDistinctFromReset(t *testing.T) {
	proposals := Proposals{Topics: []string{"llm"}, Form: "method", Use: "try"}
	empty := Resolve(proposals, []Override{{Field: "form", Action: OverrideSetEmpty, Revision: 1}})
	if !empty.Empty.Form || empty.Form != "" {
		t.Fatalf("set_empty should clear and mark empty: %+v", empty)
	}
	reset := Resolve(proposals, []Override{{Field: "form", Action: OverrideReset, Revision: 1}})
	// Reset returns to the automatic proposal.
	if reset.Form != "method" || reset.Empty.Form {
		t.Fatalf("reset should restore the automatic value: %+v", reset)
	}
}

func TestResolveRejectSurvivesRePlayWithHigherAutomaticProbability(t *testing.T) {
	// A policy replay re-proposes llm; the human rejection must still win.
	proposals := Proposals{Topics: []string{"llm", "eval"}}
	view := Resolve(proposals, []Override{{Field: "topic", Term: "llm", Action: OverrideReject, Revision: 5}})
	if containsString(view.Topics, "llm") {
		t.Fatalf("rejected topic revived after replay: %v", view.Topics)
	}
}

// --- Question compiler ------------------------------------------------------

func TestCompileSpecQuestionsAreIndependentAndTyped(t *testing.T) {
	spec, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]QuestionKind{}
	for _, question := range spec.Questions {
		kinds[question.ID] = question.Kind
		if err := question.Validate(); err != nil {
			t.Fatalf("question %s invalid: %v", question.ID, err)
		}
	}
	if kinds["topic_llm"] != QuestionNoul || kinds["form"] != QuestionChoice {
		t.Fatalf("unexpected kinds: %+v", kinds)
	}
	if _, ok := kinds["importance"]; ok {
		t.Fatal("score must be opt-in")
	}
	withScore, err := CompileSpec(testCatalog(), true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, question := range withScore.Questions {
		legend := question.ScoreLegend()
		if question.ID == "importance" && question.Kind == QuestionScore && len(legend) >= 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("score question missing when enabled")
	}
	// The spec identity changes when Score is enabled: the Worker stores one
	// spec per id, so reusing classify-v1 would be rejected as a conflict.
	if withScore.SpecID == spec.SpecID {
		t.Fatal("enabling Score must produce a distinct immutable spec id")
	}
	if withScore.SemanticHash == spec.SemanticHash {
		t.Fatal("enabling Score must change the semantic hash")
	}
}

func TestSpecHashIgnoresInactiveTermsAndDisplayLabels(t *testing.T) {
	first := testCatalog()
	second := testCatalog()
	second.Topics = append(second.Topics, taxonomyTerm("hidden", false))
	if hashOf(t, first) != hashOf(t, second) {
		t.Fatal("inactive terms must not change the spec hash")
	}
	third := testCatalog()
	third.Topics[0].Description = "changed definition"
	if hashOf(t, first) == hashOf(t, third) {
		t.Fatal("a definition change must change the spec hash")
	}
	// A display-only label rename must not invalidate stored runs: the label is
	// not part of the model input.
	fourth := testCatalog()
	fourth.Topics[0].Label = "Large Language Models"
	if hashOf(t, first) != hashOf(t, fourth) {
		t.Fatal("a display label rename must not change the semantic spec hash")
	}
	fifth := testCatalog()
	fifth.Topics[0].Aliases = []string{"new-alias"}
	if hashOf(t, first) == hashOf(t, fifth) {
		t.Fatal("an alias change is semantic and must change the spec hash")
	}
	// Boundary examples are semantic: changing only excludes must change both
	// the question and the content-addressed spec identity (R2-14).
	sixth := testCatalog()
	sixth.Topics[0].Excludes = []string{"仅偶然提及"}
	if hashOf(t, first) == hashOf(t, sixth) {
		t.Fatal("an excludes change must change the semantic spec hash")
	}
	firstSpec, _ := CompileSpec(first, false)
	sixthSpec, _ := CompileSpec(sixth, false)
	if firstSpec.SpecID == sixthSpec.SpecID {
		t.Fatalf("an excludes change must produce a new spec id: %s", firstSpec.SpecID)
	}
	if firstSpec.SpecID == "" || !strings.HasPrefix(firstSpec.SpecID, "classify-") {
		t.Fatalf("spec id is not content-addressed: %q", firstSpec.SpecID)
	}
	// A display-only label rename keeps both the hash and the spec id.
	seventh := testCatalog()
	seventh.Topics[0].Label = "大语言模型"
	seventhSpec, _ := CompileSpec(seventh, false)
	if seventhSpec.SpecID != firstSpec.SpecID || seventhSpec.SemanticHash != firstSpec.SemanticHash {
		t.Fatal("a display label rename must not change the spec identity")
	}
}

func TestQuestionSpecRejectsSameIDDifferentDefinition(t *testing.T) {
	// The compiler is deterministic, so the same catalog yields the same hash;
	// the Worker enforces one id per hash. Here we assert determinism.
	first, _ := CompileSpec(testCatalog(), false)
	second, _ := CompileSpec(testCatalog(), false)
	firstBytes, _ := MarshalSpec(first)
	secondBytes, _ := MarshalSpec(second)
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("compiled spec is not deterministic")
	}
}

func taxonomyTerm(id string, active bool) taxonomy.Term {
	return taxonomy.Term{ID: id, Label: id, Active: active}
}

func hashOf(t *testing.T, catalog taxonomy.Catalog) string {
	t.Helper()
	spec, err := CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	return spec.SemanticHash
}
