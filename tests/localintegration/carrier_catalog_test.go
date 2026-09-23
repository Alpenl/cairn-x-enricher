package localintegration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
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
	if vocabulary.DefinitionVersion != 2 {
		t.Fatalf("wrong definition version: %d", vocabulary.DefinitionVersion)
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
	actual := mustSpec(t, catalog)
	want := mustSpec(t, frozen)
	if !reflect.DeepEqual(actual, want) {
		t.Fatal("experiment differs from the actual Worker catalog compiled by the production Go client")
	}
	previousBytes, err := os.ReadFile("../../experiments/classification/reference-v1/objective-use-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := classify.DecodeSpec(previousBytes)
	if err != nil {
		t.Fatal(err)
	}
	if actual.SpecID == previous.SpecID || actual.SemanticHash == previous.SemanticHash || len(actual.Questions) != len(previous.Questions) {
		t.Fatal("carrier definition change lost semantic identity or question population")
	}
	changed := []string{}
	for i, question := range actual.Questions {
		if !reflect.DeepEqual(question, previous.Questions[i]) {
			changed = append(changed, question.ID)
		}
	}
	if !reflect.DeepEqual(changed, []string{"carriers"}) {
		t.Fatalf("changed unrelated questions: %v", changed)
	}
	for _, spec := range []classify.QuestionSpec{previous, actual} {
		if err := queue.PutQuestionSpec(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []classify.QuestionSpec{previous, actual} {
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
	if legacy.Version != catalog.Version || len(legacy.Topics) != 17 || len(legacy.Forms) != 7 || len(legacy.Uses) != 5 {
		t.Fatal("carrier semantic upgrade changed legacy vocabulary shape")
	}
	t.Logf("actual Worker definition v2 -> production catalog -> experiment spec exact match; only carriers changed; old %s and new %s separately persisted/read; legacy 17/7/5 vocabulary retained; zero model calls", previous.SpecID, actual.SpecID)
}
