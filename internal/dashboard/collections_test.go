package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type collectionFixture struct {
	fakeBackend
	path  string
	body  any
	calls int
}

func (b *collectionFixture) Collections(_ context.Context, _ string, path string, body any) (json.RawMessage, error) {
	b.path, b.body = path, body
	b.calls++
	return json.RawMessage(`{"items":[]}`), nil
}

func TestCollectionsProxyScopeOriginAndBounds(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	b := &collectionFixture{}
	s := New(context.Background(), startedTracker(), b, &fakeProcessor{}, testLogger(), 1)
	defer s.Drain(time.Second)
	for _, test := range []struct {
		path, body, origin, site string
		status                   int
	}{
		{"/api/collections", "", "", "", 200},
		{"/api/collections/invalid/operations", `{}`, "", "", 400},
		{"/api/collections/" + id + "/operations", `{}`, "https://foreign.invalid", "cross-site", 403},
		{"/api/collections/" + id + "/operations", `{}`, "https://foreign.invalid", "", 403},
		{"/api/collections/" + id + "/operations", `{}`, "https://nas-proxy.invalid", "same-origin", 200},
		{"/api/collections/" + id + "/operations", `{"note":"` + strings.Repeat("x", 65536) + `"}`, "", "", 400},
	} {
		method := http.MethodGet
		if test.body != "" {
			method = http.MethodPost
		}
		r := httptest.NewRequestWithContext(context.Background(), method, test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		before := b.calls
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
		if test.status != 200 && b.calls != before {
			t.Fatal("rejected request reached backend")
		}
		if test.status == 200 && (!strings.HasPrefix(b.path, "/api/enrichment/collections") || w.Header().Get("X-Cairn-Collections") != "1") {
			t.Fatal("wrong private proxy contract")
		}
	}
	for _, suffix := range []string{id, "bad", id + "&collection_id=" + id} {
		query, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?collection_id="+suffix, nil))
		if suffix == id {
			if err != nil || query.CollectionID != id {
				t.Fatal("lost collection filter")
			}
		} else if err == nil {
			t.Fatal("invalid collection filter accepted")
		}
	}
}
