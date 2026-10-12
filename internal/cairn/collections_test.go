package cairn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollectionsRequiresAcknowledgementAndPreservesQuery(t *testing.T) {
	ack := ""
	const id = "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cairn-Collections") != "2" {
			t.Error("missing capability request")
		}
		if r.URL.Path == "/api/enrichment/bookmarks" && r.URL.Query().Get("collection_id") != id {
			t.Error("lost collection scope")
		}
		if ack != "" {
			w.Header().Set("X-Cairn-Collections", ack)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	if _, err := client.Collections(context.Background(), "GET", "/api/enrichment/collections", nil); err == nil {
		t.Fatal("silently accepted a legacy backend")
	}
	if _, err := client.ListBookmarks(context.Background(), BookmarkQuery{CollectionID: id}); err == nil {
		t.Fatal("silently ignored collection scope")
	}
	// A v1 Worker cannot store v2 rule settings: refuse it too.
	ack = "1"
	if _, err := client.Collections(context.Background(), "GET", "/api/enrichment/collections", nil); err == nil {
		t.Fatal("silently accepted a v1 backend")
	}
	ack = "2"
	if _, err := client.Collections(context.Background(), "GET", "/api/enrichment/collections", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListBookmarks(context.Background(), BookmarkQuery{CollectionID: id}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"https://foreign.invalid/", "/api/enrichment/collections-other", "/api/enrichment/collections/../secrets"} {
		if _, err := client.Collections(context.Background(), "GET", path, nil); err == nil {
			t.Fatal("unsafe path accepted", path)
		}
	}
}
