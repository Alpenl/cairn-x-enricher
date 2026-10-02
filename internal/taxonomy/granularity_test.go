package taxonomy

import "testing"

func TestSpecificConceptMustHaveScopedDefinitionAndUnambiguousIdentity(t *testing.T) {
	scoped := Term{ID: "portrait_photography", Label: "写真", Active: true, Granularity: "specific",
		Description: "人物写真制作", Includes: []string{"人像写真制作"}, Excludes: []string{"仅照片配图"}, RecallTerms: []string{"写真", "photo shoot", "photoshoot"}}
	base := Catalog{Version: "test", Topics: []Term{scoped}, Forms: []Term{{ID: "method", Label: "方法", Active: true}}, Uses: []Term{{ID: "try", Label: "待试", Active: true}}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Term){
		func(term *Term) { term.Description = "" }, func(term *Term) { term.Includes = nil },
		func(term *Term) { term.Excludes = nil }, func(term *Term) { term.RecallTerms = nil },
		func(term *Term) { term.Granularity = "invented" },
		func(term *Term) { term.RecallTerms = []string{"portrait", " PORTRAIT "} },
		func(term *Term) { term.Relations = []TermRelation{{ID: "portrait_photography", Kind: "related"}} },
	} {
		changed := base
		changed.Topics = []Term{scoped}
		mutate(&changed.Topics[0])
		if changed.Validate() == nil {
			t.Fatal("unreviewed or malformed new concept entered the automatic catalog")
		}
	}
	duplicate := base
	duplicate.Topics = append([]Term{scoped}, Term{ID: "other_portrait", Label: "另一写真", Active: true, Aliases: []string{"写真"}})
	if duplicate.Validate() == nil {
		t.Fatal("a duplicate concept name passed identity checks")
	}
	legacy := base
	legacy.Topics = []Term{{ID: "llm", Label: "LLM", Active: true}}
	if err := legacy.Validate(); err != nil || legacy.Topics[0].Specific() {
		t.Fatal("legacy terms lost their broad default")
	}
}
