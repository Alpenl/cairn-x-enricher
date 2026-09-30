package classify

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func frozenFieldCatalog(t *testing.T) taxonomy.Catalog {
	t.Helper()
	raw, err := os.ReadFile("../../experiments/classification/reference-v1/taxonomy.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestQuestionStateFieldsMatchActualProductionWire(t *testing.T) {
	client, err := NewClient("https://offline.invalid", "fixture", "jev-1.13.0", nil, frozenFieldCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := client.BuildProviderRequest(Input{OriginalText: "A source about a concrete method.", ContextText: "A quoted reply about an unrelated subject."})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		State     map[string]json.RawMessage `json:"state"`
		Questions map[string]struct {
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"primary", "context"} {
		if _, ok := request.State[key]; !ok {
			t.Fatalf("actual state missing %s", key)
		}
	}
	for id, q := range request.Questions {
		if !strings.Contains(q.Instructions, "`primary`") || !strings.Contains(q.Instructions, "`context`") || strings.Contains(q.Instructions, "`original_text`") || strings.Contains(q.Instructions, "`context_text`") {
			t.Errorf("question %s refers to fields absent from actual state", id)
		}
	}
}

func TestStateFieldAblationChangesOnlyTwoFieldNames(t *testing.T) {
	oldBytes, err := os.ReadFile("../../experiments/classification/reference-v1/baseline-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	old, err := DecodeSpec(oldBytes)
	if err != nil {
		t.Fatal(err)
	}
	nextBytes, err := os.ReadFile("../../experiments/classification/reference-v1/state-fields-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	next, err := DecodeSpec(nextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if next.SpecID != "classify-0b02fbfce85d" || next.SemanticHash != "c760533cfd880ba0201f97d41c3a1d4d6856bce6c816feb30328d160a7cdead5" {
		t.Fatal("field-name correction must produce a new immutable spec")
	}
	if len(next.Questions) != len(old.Questions) {
		t.Fatal("changed the question population")
	}
	for i, q := range next.Questions {
		var text string
		if err := json.Unmarshal(q.Instructions, &text); err != nil {
			t.Fatal(err)
		}
		text = strings.ReplaceAll(strings.ReplaceAll(text, "`primary`", "`original_text`"), "`context`", "`context_text`")
		q.Instructions = mustJSON(text)
		if !reflect.DeepEqual(q, old.Questions[i]) {
			t.Fatalf("uncontrolled change in %s", q.ID)
		}
	}
	if old.SpecID != "classify-80156c157660" || old.SemanticHash != "2a39cb299aa0bf916bb4ac9642c1e515dd07f35883a61da403b4c53c05ce4d26" {
		t.Fatal("historical baseline changed")
	}
}
