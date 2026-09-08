package taxonomy

import (
	"reflect"
	"strings"
	"testing"
)

func testCatalog() Catalog {
	return Catalog{
		Version: "v1",
		Topics: []Term{
			{ID: "llm", Label: "LLM", Aliases: []string{"AI", "大模型"}, Active: true},
			{ID: "eng", Label: "工程", Active: true},
			{ID: "eval", Label: "评估", Active: true},
			{ID: "design", Label: "设计", Active: true},
			{ID: "retired", Label: "旧标签", Active: false},
		},
		Forms: []Term{{ID: "tool", Label: "工具", Aliases: []string{"app"}, Active: true}},
		Uses:  []Term{{ID: "try", Label: "待试", Active: true}},
	}
}

func TestNormalizeControlsTagsWithoutPromotingEntities(t *testing.T) {
	catalog := testCatalog()
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	result := catalog.Normalize(Classification{
		Selection: Selection{Topics: []string{" Ａ Ｉ ", "大模型", "retired", "GPT-7", "工程", "评估", "设计"}, Form: " APP ", Use: "待试"},
		Entities:  []string{"GPT-7", " GPT-7 ", "项目甲"}, WhySuggestion: " 用于项目选型 ", TaxonomyVersion: "invented",
	})
	if !reflect.DeepEqual(result.Topics, []string{"llm", "eng", "eval"}) || result.Form != "tool" || result.Use != "try" || !result.Uncertainty {
		t.Fatalf("classification = %+v", result)
	}
	if !reflect.DeepEqual(result.DiscardedTags, []string{"retired", "gpt-7"}) || !reflect.DeepEqual(result.Entities, []string{"GPT-7", "项目甲"}) || result.TaxonomyVersion != "v1" || result.WhySuggestion != "用于项目选型" {
		t.Fatalf("audit fields = %+v", result)
	}
	result = catalog.Normalize(Classification{Selection: Selection{Topics: []string{"LLM"}, Form: "tool", Use: "try"}})
	if result.Uncertainty || len(result.DiscardedTags) != 0 {
		t.Fatalf("valid classification marked uncertain: %+v", result)
	}
}

func TestIncompleteClassificationNeedsReview(t *testing.T) {
	for _, raw := range []Classification{
		{},
		{Selection: Selection{Topics: []string{"new topic"}, Form: "tool", Use: "try"}},
		{Selection: Selection{Topics: []string{"llm"}, Form: "invented", Use: "try"}},
		{Selection: Selection{Topics: []string{"llm"}, Form: "tool", Use: ""}},
	} {
		result := testCatalog().Normalize(raw)
		if !result.Uncertainty || result.Topics == nil || result.Entities == nil || result.DiscardedTags == nil {
			t.Fatalf("incomplete classification = %+v", result)
		}
	}
}

func TestCatalogRejectsAmbiguousAliasesAndHumanEdits(t *testing.T) {
	catalog := testCatalog()
	catalog.Topics[1].Aliases = []string{"ａｉ"}
	if err := catalog.Validate(); err == nil {
		t.Fatal("ambiguous alias accepted")
	}
	for _, selection := range []Selection{
		{Topics: []string{"AI"}}, {Topics: []string{"retired"}}, {Topics: []string{"llm", "llm"}},
		{Topics: []string{"llm", "eng", "eval", "design"}}, {Form: "app"}, {Use: "random"},
	} {
		if err := testCatalog().ValidateSelection(selection); err == nil {
			t.Fatalf("invalid human edit accepted: %+v", selection)
		}
	}
	if err := testCatalog().ValidateSelection(Selection{Topics: []string{}}); err != nil {
		t.Fatalf("explicit empty selection rejected: %v", err)
	}
}

func TestNormalizeBoundsFreeTextAndAudit(t *testing.T) {
	raw := Classification{WhySuggestion: strings.Repeat("字", 300), Entities: []string{strings.Repeat("名", 100)}}
	for range 100 {
		raw.Topics = append(raw.Topics, strings.Repeat("无", 100))
	}
	result := testCatalog().Normalize(raw)
	if len([]rune(result.WhySuggestion)) != 200 || len([]rune(result.Entities[0])) != 80 || len(result.DiscardedTags) != 1 || !result.Uncertainty {
		t.Fatalf("unbounded classification: %+v", result)
	}
}
