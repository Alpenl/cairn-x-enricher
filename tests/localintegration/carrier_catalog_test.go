package localintegration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerCarrierDefinitionUpgrade(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	vocabulary, err := queue.GetV2Taxonomy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vocabulary.DefinitionVersion != 4 || vocabulary.Version != "2026-09-30.2" {
		t.Fatalf("wrong current taxonomy identity: %s definition v%d", vocabulary.Version, vocabulary.DefinitionVersion)
	}
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../experiments/classification/reference-v1/carrier-boundaries-taxonomy.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen taxonomy.Catalog
	if err := json.Unmarshal(fixture, &frozen); err != nil {
		t.Fatal(err)
	}
	// This fixture freezes the historical carrier-only semantic upgrade. The
	// live catalog has since added personal topics and resources, so comparing
	// the entire live spec with that historical experiment would erase its role.
	if frozen.DefinitionVersion != 2 || frozen.Version != "2026-09-20.1" {
		t.Fatal("historical carrier experiment identity changed")
	}
	historical := mustSpec(t, frozen)
	current := mustSpec(t, catalog)
	if catalog.DefinitionVersion != vocabulary.DefinitionVersion || catalog.Version != vocabulary.Version ||
		current.TaxonomyVersion != vocabulary.Version || len(current.Questions) != 31 {
		t.Fatal("current Worker v4 catalog did not compile its 31-question spec")
	}
	for dimension, expected := range map[string][]string{
		"topics": {"ai_coding", "agent_workflow", "image_creation", "video_creation", "writing_creation", "ui_design",
			"knowledge_workflow", "information_sources", "model_practice", "creator_business", "finance_resources", "document_layout", "clothing_style"},
		"resource_kinds": {"skill", "prompt", "software", "component", "model", "reference"},
	} {
		terms := catalog.Topics
		prefix, questionDimension := "topic_", "topic"
		if dimension == "resource_kinds" {
			terms = catalog.ResourceKinds
			prefix, questionDimension = "resource_kind_", dimension
		}
		active := []string{}
		for _, term := range terms {
			if !term.Active {
				continue
			}
			active = append(active, term.ID)
			found := false
			for _, question := range current.Questions {
				if question.ID == prefix+term.ID && question.Kind == classify.QuestionNoul && question.Dimension == questionDimension && question.TermID == term.ID {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("current spec lost independent %s question for %s", dimension, term.ID)
			}
		}
		sort.Strings(active)
		sort.Strings(expected)
		if !reflect.DeepEqual(active, expected) {
			t.Fatalf("wrong current active %s: %v", dimension, active)
		}
	}
	if !reflect.DeepEqual(catalog.Carriers, frozen.Carriers) {
		t.Fatal("current catalog changed the frozen carrier boundaries")
	}
	for _, question := range current.Questions {
		if question.ID != "carriers" {
			continue
		}
		found := false
		for _, previousQuestion := range historical.Questions {
			if previousQuestion.ID == "carriers" && reflect.DeepEqual(question, previousQuestion) {
				found = true
			}
		}
		if !found {
			t.Fatal("current spec changed the historical carrier question")
		}
	}
	previousBytes, err := os.ReadFile("../../experiments/classification/reference-v1/objective-use-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := classify.DecodeSpec(previousBytes)
	if err != nil {
		t.Fatal(err)
	}
	if historical.SpecID == previous.SpecID || historical.SemanticHash == previous.SemanticHash || len(historical.Questions) != len(previous.Questions) {
		t.Fatal("carrier definition change lost semantic identity or question population")
	}
	changed := []string{}
	for i, question := range historical.Questions {
		if !reflect.DeepEqual(question, previous.Questions[i]) {
			changed = append(changed, question.ID)
		}
	}
	if !reflect.DeepEqual(changed, []string{"carriers"}) {
		t.Fatalf("changed unrelated questions: %v", changed)
	}
	if current.SpecID == historical.SpecID || current.SemanticHash == historical.SemanticHash {
		t.Fatal("current v4 and historical carrier-only v2 lost separate spec identities")
	}
	for _, spec := range []classify.QuestionSpec{previous, historical, current} {
		if err := queue.PutQuestionSpec(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []classify.QuestionSpec{previous, historical, current} {
		saved, err := queue.GetQuestionSpec(ctx, spec.SpecID)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := classify.DecodeSpec(saved.Payload)
		if err != nil || !reflect.DeepEqual(restored, spec) {
			t.Fatalf("historical/current spec did not round-trip separately: %v", err)
		}
	}
	legacy, err := queue.GetTaxonomy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Version != frozen.Version || len(legacy.Topics) != 17 || len(legacy.Forms) != 7 || len(legacy.Uses) != 5 {
		t.Fatal("carrier semantic upgrade changed legacy vocabulary shape")
	}
	t.Logf("frozen v2 carrier experiment changed only carriers from %s to %s; current Worker v4 compiles 13 topics + 6 resources into separate 31-question spec %s; all three persisted/read independently; legacy 17/7/5 vocabulary retained; zero model calls", previous.SpecID, historical.SpecID, current.SpecID)
}
