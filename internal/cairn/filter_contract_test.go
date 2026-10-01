package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientEffectiveFiltersRequireConfirmedContract(t *testing.T) {
	for _, version := range []string{"", "0", "1", "2", "null"} {
		t.Run("version="+version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := map[string]string{"topics": "llm,design", "content_functions": "method,data", "carriers": "single,external_article", "affordances": "practice", "entity_state": "failed,stale", "filter_contract_version": "1", "before_id": "99"}
				for key, value := range want {
					if r.URL.Query().Get(key) != value {
						t.Errorf("%s = %q, want %q", key, r.URL.Query().Get(key), value)
					}
				}
				suffix := ""
				if version != "" {
					suffix = ",\"filter_contract_version\":" + version
				}
				_, _ = fmt.Fprintf(w, `{"items":[],"next_before_id":null,"counts":{}%s}`, suffix)
			}))
			defer server.Close()
			client := NewClient(server.URL, "token", server.Client())
			page, err := client.ListBookmarks(context.Background(), BookmarkQuery{Topics: []string{"llm", "design"}, ContentFunctions: []string{"method", "data"}, Carriers: []string{"single", "external_article"}, Affordances: []string{"practice"}, EntityStates: []string{"failed", "stale"}, BeforeID: 99})
			if version == "1" {
				if err != nil || page.FilterContractVersion == nil || *page.FilterContractVersion != 1 {
					t.Fatalf("confirmed page=%+v err=%v", page, err)
				}
				return
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_filter_contract" {
				t.Fatalf("unconfirmed backend err=%v", err)
			}
		})
	}
}

func TestClientLegacyListKeepsOldShapeAndOptInIsExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("filter_contract_version") {
			t.Error("legacy request unexpectedly negotiated v2 filters")
		}
		_, _ = fmt.Fprint(w, `{"items":[],"next_before_id":null,"counts":{}}`)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "token", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{Topic: "llm"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientFunctionsIntersectionRequiresBothCapabilities(t *testing.T) {
	for _, capabilities := range []string{"none", "tags", "functions", "both"} {
		t.Run(capabilities, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("functions_mode") != "all" || r.URL.Query().Get("content_functions") != "method,case" || r.URL.Query().Get("filter_contract_version") != "1" {
					t.Errorf("intersection request lost: %s", r.URL.RawQuery)
				}
				if capabilities == "tags" || capabilities == "both" {
					w.Header().Set("X-Cairn-Tag-System", "1")
				}
				if capabilities == "functions" || capabilities == "both" {
					w.Header().Set("X-Cairn-Content-Functions", "1")
				}
				_, _ = fmt.Fprint(w, `{"items":[],"next_before_id":null,"counts":{},"filter_contract_version":1}`)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "token", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{ContentFunctions: []string{"method", "case"}, FunctionsMode: "all"})
			if capabilities == "both" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_tag_filter_contract" {
					t.Fatalf("missing capability accepted: %v", err)
				}
			}
		})
	}
}
