package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefinementsPreserveBroadAnyAndRequireGranularityCapability(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Cairn-Topic-Granularity") != "1" || r.Header.Get("X-Cairn-Tag-System") != "1" {
					t.Error("refinement request omitted required capability")
				}
				if r.URL.Query().Get("topics") != "image_creation,video_creation" || r.URL.Query().Get("topics_mode") != "any" ||
					r.URL.Query().Get("topic_refinements") != "portrait_photography,character_consistency" || r.URL.Query().Get("filter_contract_version") != "1" {
					t.Error("specific refinement overwrote broad ANY filter semantics")
				}
				w.Header().Set("X-Cairn-Tag-System", "1")
				if acknowledged {
					w.Header().Set("X-Cairn-Topic-Granularity", "1")
				}
				_, _ = fmt.Fprint(w, `{"items":[],"next_before_id":null,"counts":{},"filter_contract_version":1}`)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "fixture", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{
				Topics: []string{"image_creation", "video_creation"}, TopicMode: "any",
				TopicRefinements: []string{"portrait_photography", "character_consistency"},
			})
			if acknowledged {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_topic_refinement_contract" {
					t.Fatalf("server silently ignored specific conjunction: %v", err)
				}
			}
		})
	}
}

func TestGranularTaxonomyKeepsScopedMetadataAndRetiredIdentity(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Cairn-Topic-Granularity") != "1" {
					t.Error("taxonomy request omitted capability")
				}
				w.Header().Set("X-Cairn-Tag-System", "1")
				if acknowledged {
					w.Header().Set("X-Cairn-Topic-Granularity", "1")
				}
				_, _ = fmt.Fprint(w, `{"version":"2026-10-02.1","definition_version":5,"topics":[{"id":"image_creation","label":"图像生成","active":true,"aliases":[],"granularity":"broad","navigation":true},{"id":"portrait_photography","label":"写真","active":true,"aliases":[],"description":"人物写真制作","includes":["人像写真制作"],"excludes":["仅照片配图"],"granularity":"specific","navigation":false,"recall_terms":["写真"],"relations":[{"id":"image_creation","kind":"related"}]},{"id":"llm","label":"LLM","active":false,"deprecated":true,"aliases":[]}],"forms":[{"id":"method","label":"方法","active":true,"aliases":[]}],"uses":[{"id":"try","label":"待试","active":true,"aliases":[]}]}`)
			}))
			defer server.Close()
			catalog, err := NewClient(server.URL, "fixture", server.Client()).GetV2Catalog(context.Background())
			if !acknowledged {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_topic_granularity_contract" {
					t.Fatalf("new metadata without negotiated contract was consumed: %v", err)
				}
				return
			}
			if err != nil || !catalog.Topics[0].Navigation || !catalog.Topics[1].Specific() || catalog.Topics[1].Navigation ||
				len(catalog.Topics[1].RecallTerms) != 1 || len(catalog.Topics[1].Relations) != 1 || catalog.Topics[2].Active {
				t.Fatalf("term granularity or historical identity was lost: %+v %v", catalog, err)
			}
		})
	}
}

func TestRefinedCountsAndExportRequireAcknowledgedConjunction(t *testing.T) {
	for _, path := range []string{"/api/v2/tags/counts?topics=image_creation&topic_refinements=portrait_photography", "/api/v2/tags/export?topic_refinements=portrait_photography"} {
		for _, acknowledged := range []bool{false, true} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Cairn-Tag-System", "1")
				if acknowledged {
					w.Header().Set("X-Cairn-Topic-Granularity", "1")
				}
				_, _ = fmt.Fprint(w, `{}`)
			}))
			_, err := NewClient(server.URL, "fixture", server.Client()).GetV2TagSystem(context.Background(), path)
			server.Close()
			if acknowledged && err != nil {
				t.Fatal(err)
			}
			if !acknowledged {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_topic_refinement_contract" {
					t.Fatalf("counts/export silently ignored a fine topic: %v", err)
				}
			}
		}
	}
}
