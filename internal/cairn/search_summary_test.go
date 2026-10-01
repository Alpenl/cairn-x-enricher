package cairn

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchSummaryNegotiationKeepsExcerptAndFallsBackOnceForLegacy(t *testing.T) {
	for _, modern := range []bool{true, false} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("X-Cairn-Search-Summary") != "1" || r.URL.Query().Get("q") != "needle" || r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("before_id") != "8" {
					t.Error("lost negotiated search identity")
				}
				field := `"original_text":"before needle after"`
				if modern {
					w.Header().Set("X-Cairn-Search-Summary", "1")
					field = `"search_excerpt":"needle excerpt"`
				}
				if !modern && calls == 1 && r.URL.Query().Get("view") != "summary" {
					t.Error("first request wasn't bounded summary")
				}
				if !modern && calls == 2 && r.URL.Query().Has("view") {
					t.Error("legacy retry stayed summary")
				}
				_, _ = fmt.Fprintf(w, `{"items":[{"id":1,"url":"https://x.com/u/status/1","status":"completed",%s}],"counts":{},"next_before_id":null}`, field)
			}))
			defer server.Close()
			page, err := NewClient(server.URL, "fixture", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{Search: "needle", SummaryOnly: true, Limit: 3, BeforeID: 8})
			if err != nil {
				t.Fatal(err)
			}
			if modern && (calls != 1 || page.Items[0].SearchExcerpt != "needle excerpt" || page.Items[0].OriginalText != "") {
				t.Fatalf("modern summary dropped excerpt: calls=%d page=%+v", calls, page)
			}
			if !modern && (calls != 2 || page.Items[0].OriginalText == "") {
				t.Fatalf("legacy fallback lost body match: calls=%d page=%+v", calls, page)
			}
		})
	}
}

func TestNegotiatedSearchStillRejectsUnknownFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Cairn-Search-Summary", "1")
		_, _ = fmt.Fprint(w, `{"items":[{"id":1,"url":"https://x.com/u/status/1","status":"completed","search_excerpt":"needle","unknown_extra":1}],"counts":{}}`)
	}))
	defer server.Close()
	if _, err := NewClient(server.URL, "fixture", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{Search: "needle", SummaryOnly: true}); err == nil {
		t.Fatal("search capability disabled strict decoding")
	}
}
