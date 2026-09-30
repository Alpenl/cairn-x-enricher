package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTagFiltersRequireAcknowledgedServerCapability(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Cairn-Tag-System") != "1" {
					t.Error("new client did not declare tag capability")
				}
				for key, value := range map[string]string{"topics": "image_creation,model_practice", "resource_kinds": "skill,model", "custom_tags": "123e4567-e89b-12d3-a456-426614174000", "topics_mode": "all", "resource_mode": "any", "custom_mode": "all", "filter_contract_version": "1"} {
					if r.URL.Query().Get(key) != value {
						t.Errorf("%s=%q want=%q", key, r.URL.Query().Get(key), value)
					}
				}
				if acknowledged {
					w.Header().Set("X-Cairn-Tag-System", "1")
				}
				_, _ = fmt.Fprint(w, `{"items":[],"next_before_id":null,"counts":{},"filter_contract_version":1}`)
			}))
			defer server.Close()
			query := BookmarkQuery{Topics: []string{"image_creation", "model_practice"}, ResourceKinds: []string{"skill", "model"}, CustomTags: []string{"123e4567-e89b-12d3-a456-426614174000"}, TopicMode: "all", ResourceMode: "any", CustomMode: "all"}
			_, err := NewClient(server.URL, "fixture", server.Client()).ListBookmarks(context.Background(), query)
			if acknowledged {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_tag_filter_contract" {
					t.Fatalf("old server silently returned an unfiltered page: %v", err)
				}
			}
		})
	}
}

func TestTagSystemBridgePreservesOperationAndHistory(t *testing.T) {
	operation := json.RawMessage(`{"operation_key":"replace-image","expected_revision":12,"operations":[{"action":"replace","from_tag_ref":"system/topics/ai_coding","to_tag_ref":"system/topics/image_creation"}]}`)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-Cairn-Tag-System") != "1" {
			t.Error("tag bridge lacks capability header")
		}
		if r.Method == http.MethodPost {
			var body json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body) != string(operation) {
				t.Error("bridge replaced the user operation or lost its revision")
			}
		}
		_, _ = fmt.Fprint(w, `{"id":8,"revision":13,"events":[{"actor_type":"human","action":"replace","before_state":"automatic","after_state":"accepted"}]}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	result, err := client.MutateV2TagSystem(context.Background(), http.MethodPost, "/api/v2/links/8/tags", operation)
	if err != nil || !json.Valid(result) {
		t.Fatalf("mutation=%s err=%v", result, err)
	}
	if _, err := client.GetV2TagSystem(context.Background(), "/api/v2/links/8/tag-history?before_id=11"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"https://outside.invalid/api/v2/tags", "/api/v2/../credentials", "/api/enrichment/jobs"} {
		if _, err := client.GetV2TagSystem(context.Background(), path); err == nil {
			t.Fatal("generic tag bridge accepted a foreign or non-v2 route")
		}
	}
	if requests != 2 {
		t.Fatalf("invalid routes reached upstream or bridge retried an operation: %d requests", requests)
	}
}

func TestTagCatalogReadsVersionsAndKeepsRetiredTermsOutOfInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cairn-Tag-System") != "1" {
			t.Error("catalog request did not negotiate tags")
		}
		_, _ = fmt.Fprint(w, `{"version":"2026-09-30.1","definition_version":3,"topics":[{"id":"image_creation","label":"图像生成","active":true,"status":"active","aliases":[],"description":"生图和编辑任务","definition_version":1,"display_revision":2},{"id":"llm","label":"LLM","active":false,"deprecated":true,"status":"deprecated","aliases":[],"definition_version":1,"display_revision":1}],"resource_kinds":[{"id":"skill","label":"Skill","active":true,"status":"active","aliases":[],"description":"可复用技能包","definition_version":1,"display_revision":1}],"forms":[{"id":"method","label":"方法","active":true,"aliases":[]}],"uses":[{"id":"quote","label":"可引用","active":true,"aliases":[]}]}`)
	}))
	defer server.Close()
	catalog, err := NewClient(server.URL, "fixture", server.Client()).GetV2Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.DefinitionVersion != 3 || len(catalog.ResourceKinds) != 1 || catalog.ResourceKinds[0].DefinitionVersion != 1 || catalog.Topics[0].DisplayRevision != 2 || catalog.Topics[1].Active {
		t.Fatalf("tag identity or retirement was lost: %+v", catalog)
	}
}

func TestNegotiatedBookmarkReadsEffectiveResourcesAndCustomDefinitions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Cairn-Tag-System", "1")
		_, _ = fmt.Fprint(w, `{"items":[{"id":8,"url":"https://example.com/skill","note":"","created_at":"2026-09-30T00:00:00Z","status":"completed","attempts":0,"related_links":[],"images":[],"paid_call_unresolved":false,"why":"","curation_status":"inbox","classification_reviewed":false,"classification":{"topics":["image_creation"],"form":"method","use":"","resource_kinds":["skill"],"entities":[],"uncertainty":false,"taxonomy_version":"2026-09-30.1","discarded_tags":[],"why_suggestion":""},"custom_tags":[{"id":"uuid","tag_ref":"custom/owner/uuid","label":"周末试试","revision":2,"status":"active","owner_id":"owner"}]}],"counts":{},"next_before_id":null}`)
	}))
	defer server.Close()
	page, err := NewClient(server.URL, "fixture", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || len(page.Items[0].Classification.ResourceKinds) != 1 || len(page.Items[0].CustomTags) != 1 || page.Items[0].CustomTags[0].Revision != 2 {
		t.Fatal("negotiated effective resource or custom identity disappeared")
	}
}
