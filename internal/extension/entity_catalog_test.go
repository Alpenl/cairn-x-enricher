package extension

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func testEntityCatalog(t *testing.T) EntityCatalog {
	t.Helper()
	catalog, err := DecodeEntityCatalog([]byte(`{"version":"fixture-1","entities":[
 {"id":"acme-a","label":"Acme","kind":"project","aliases":[],"identifiers":["https://example.com/a"]},
 {"id":"acme-b","label":"Acme","kind":"organization","aliases":[],"identifiers":["https://example.com/b"]}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func entityChoice(selected string, options ...string) classify.RawAnswer {
	values := map[string]float64{}
	for _, option := range options {
		values[option] = 0
	}
	values[selected] = 1
	confidence := 1.0
	return classify.RawAnswer{Type: classify.TypeChoice, Choice: &classify.ChoiceAnswer{Choice: selected, Probabilities: values}, Confidence: &confidence}
}

func TestCanonicalOptionsRequireIdentityInTheSameSourceBlock(t *testing.T) {
	catalog := testEntityCatalog(t)
	blocks := []Block{{ID: "a", Text: "Acme", URL: "https://example.com/a"}, {ID: "b", Text: "Acme", URL: "https://example.com/b"}, {ID: "unbound", Text: "Acme"}}
	candidates := ExtractCandidates(blocks, nil)
	if len(candidates) != 3 {
		t.Fatalf("same-name source occurrences were collapsed: %+v", candidates)
	}
	for _, candidate := range candidates {
		options := catalog.options(candidate, blocks)
		switch candidate.BlockID {
		case "a", "b":
			if len(options) != 1 || options[0].Entity.ID != "acme-"+candidate.BlockID {
				t.Fatalf("cross-block identity leak: %+v", options)
			}
		case "unbound":
			if len(options) != 0 {
				t.Fatalf("name alone authorized a merge: %+v", options)
			}
		}
	}
	candidate := SurfaceCandidate{Surface: "Acme", BlockID: "a", Kind: "surface"}
	for _, text := range []string{"Acme https://example.com/ab", "Acme https://evil.example/a", "Acme http://example.com/a"} {
		if got := catalog.options(candidate, []Block{{ID: "a", Text: text}}); len(got) != 0 {
			t.Fatalf("loose URL match %q: %+v", text, got)
		}
	}
	if got := catalog.options(candidate, []Block{{ID: "a", Text: "[Acme](https://example.com/a)"}}); len(got) != 1 {
		t.Fatalf("explicit identifier missing: %+v", got)
	}
}

func TestEntityServiceKeepsSameNameIdentitiesAndUnknownSeparate(t *testing.T) {
	catalog := testEntityCatalog(t)
	blocks := []Block{{ID: "a", Text: "Acme", URL: "https://example.com/a"}, {ID: "b", Text: "Acme", URL: "https://example.com/b"}, {ID: "c", Text: "Acme"}}
	relevance := []string{"relevant", "incidental", "none", "unknown"}
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{
		"entity_0": entityChoice("relevant", relevance...), "canonical_0": entityChoice("id:acme-a", "id:acme-a", "none", "unknown"),
		"entity_1": entityChoice("relevant", relevance...), "canonical_1": entityChoice("id:acme-b", "id:acme-b", "none", "unknown"),
		"entity_2": entityChoice("relevant", relevance...),
	}}
	service := NewService(Flags{Entities: true}, DefaultBudget(), judge)
	if err := service.SetEntityCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Entities[0].Label = "caller mutation"
	result := service.Entities(context.Background(), blocks, nil)
	if result.State != EntityCompletedNonempty || len(result.Entities) != 1 || result.Entities[0] != "Acme" || len(result.Observations) != 3 || judge.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, judge.calls)
	}
	if result.Observations[0].CanonicalID != "acme-a" || result.Observations[1].CanonicalID != "acme-b" || result.Observations[2].CanonicalState != "unknown" {
		t.Fatalf("identities=%+v", result.Observations)
	}
	for _, observation := range result.Observations[:2] {
		if observation.CanonicalLabel != "Acme" || len(observation.CanonicalEvidence) != 1 {
			t.Fatalf("provenance lost: %+v", observation)
		}
	}
	if len(judge.lastQ) != 5 {
		t.Fatalf("unexpected fan-out %d", len(judge.lastQ))
	}
}

func TestEntityJudgmentsPreserveExplicitNoneUnknownAndIncidental(t *testing.T) {
	for _, decision := range []string{"none", "unknown", "incidental"} {
		t.Run(decision, func(t *testing.T) {
			judge := &fakeJudge{answers: map[string]classify.RawAnswer{"entity_0": entityChoice(decision, "relevant", "incidental", "none", "unknown")}}
			result := NewService(Flags{Entities: true}, DefaultBudget(), judge).Entities(context.Background(), []Block{{ID: "p", Text: "Acme"}}, nil)
			if result.State != EntityCompletedEmpty || len(result.Observations) != 1 || result.Observations[0].Decision != decision {
				t.Fatalf("collapsed explicit decision: %+v", result)
			}
		})
	}
}

func TestEntityCatalogRejectsMalformedAndUnboundedOperatorData(t *testing.T) {
	catalog := testEntityCatalog(t)
	valid, _ := json.Marshal(catalog)
	for _, raw := range []string{
		strings.Replace(string(valid), `"version":`, `"unexpected":true,"version":`, 1),
		strings.Replace(string(valid), `"acme-b"`, `"acme-a"`, 1),
		strings.Replace(string(valid), `https://example.com/a`, `https://user:password@example.com/a`, 1),
		strings.Replace(string(valid), `https://example.com/b`, `https://example.com/a`, 1),
		strings.Replace(string(valid), `"project"`, `"made-up-kind"`, 1),
		string(valid) + `{}`, strings.Repeat("x", maxEntityCatalogBytes+1),
	} {
		if _, err := DecodeEntityCatalog([]byte(raw)); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
}

func TestEntityCandidatesNeverInventATruncatedLongName(t *testing.T) {
	judge := &fakeJudge{}
	result := NewService(Flags{Entities: true}, DefaultBudget(), judge).Entities(context.Background(), []Block{{ID: "p", Text: strings.Repeat("长", 121)}}, []string{"https://example.com/" + strings.Repeat("a", 130)})
	if result.State != EntityCompletedEmpty || judge.calls != 0 {
		t.Fatalf("overlong unsupported term reached model: %+v", result)
	}
	if len(ExtractCandidates([]Block{{ID: "p", Text: strings.Repeat("𐐀", 61)}}, nil)) != 0 {
		t.Fatal("non-BMP name exceeded stored UTF-16 bound")
	}
}
