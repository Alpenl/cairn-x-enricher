package dashboard

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestTopicTaxonomyMetadataRequiresNegotiation(t *testing.T) {
	catalog := cairn.V2Taxonomy{Topics: []cairn.TaxonomyTerm{
		{ID: "image_creation", Label: "图像生成", Granularity: "broad", Navigation: true},
		{ID: "portrait", Label: "写真", Granularity: "specific", RecallTerms: []string{"AI写真"}},
	}}
	for _, aware := range []bool{false, true} {
		request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v2-taxonomy", nil)
		if aware {
			request.Header.Set("X-Cairn-Tag-System", "1")
			request.Header.Set("X-Cairn-Topic-Granularity", "1")
		}
		response := httptest.NewRecorder()
		writeTopicTaxonomy(response, request, catalog)
		var payload struct {
			Topics []map[string]any `json:"topics"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		for _, term := range payload.Topics {
			if _, leaked := term["recall_terms"]; leaked {
				t.Fatal("classifier recall terms leaked into UI")
			}
			_, metadata := term["granularity"]
			if metadata != aware {
				t.Fatalf("metadata awareness=%v, body=%s", aware, response.Body)
			}
		}
		if aware && (response.Header().Get("X-Cairn-Topic-Granularity") != "1" || payload.Topics[1]["navigation"] != false) {
			t.Fatal("specific topic must acknowledge metadata with explicit navigation=false")
		}
		if !aware && response.Header().Get("X-Cairn-Topic-Granularity") != "" {
			t.Fatal("unnegotiated capability")
		}
	}
	if catalog.Topics[1].RecallTerms[0] != "AI写真" {
		t.Fatal("UI projection mutated source catalog")
	}
}

func TestTopicRefinementQueryPreservesOriginalAnyGroup(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?topics=image_creation,design&topic_refinements=portrait,avatar&filter_contract_version=1", nil)
	query, err := bookmarkQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Topics) != 2 || len(query.TopicRefinements) != 2 || query.TopicMode != "" {
		t.Fatalf("independent refinement changed original ANY group: %#v", query)
	}
	if !query.NeedsFilterContract() {
		t.Fatal("refinement must negotiate filter support")
	}
	for _, query := range []string{"topic_refinements=portrait,,avatar", "topic_refinements=portrait&topic_refinements=avatar"} {
		if _, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?"+query, nil)); err == nil {
			t.Fatalf("accepted malformed query %s", query)
		}
	}
}
