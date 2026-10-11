package cairn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnifiedTagFilterIsForwardedAndAcknowledged(t *testing.T) {
	ack := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("tags") != "system/topics/ui_design,custom/default/x" || q.Get("tags_mode") != "all" || q.Get("tags_children") != "1" || q.Get("filter_contract_version") != "1" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		if ack {
			w.Header().Set("X-Cairn-Tag-System", "1")
		}
		_, _ = w.Write([]byte(`{"items":[],"filter_contract_version":1}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	query := BookmarkQuery{Tags: []string{"system/topics/ui_design", "custom/default/x"}, TagsMode: "all", TagsChildren: true}
	if _, err := client.ListBookmarks(context.Background(), query); err == nil {
		t.Fatal("silently accepted a backend that ignores tag filters")
	}
	ack = true
	if _, err := client.ListBookmarks(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}

func TestPreferencesClientAllowsOnlyExactPaths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{},"revision":0}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	if _, err := client.Preferences(context.Background(), "GET", "/api/enrichment/preferences", nil); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"POST", "/api/enrichment/preferences"}, {"GET", "/api/enrichment/preferences/operations"}, {"GET", "/api/enrichment/jobs"}, {"GET", "/api/enrichment/preferences/../jobs"}} {
		if _, err := client.Preferences(context.Background(), bad[0], bad[1], nil); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
