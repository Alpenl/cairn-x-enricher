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

type redesignFixture struct {
	collectionFixture
	method, prefPath, tagPath string
	prefBody                  any
	prefCalls, tagCalls       int
}

func (b *redesignFixture) Preferences(_ context.Context, method, path string, body any) (json.RawMessage, error) {
	b.method, b.prefPath, b.prefBody = method, path, body
	b.prefCalls++
	return json.RawMessage(`{"value":{},"revision":0}`), nil
}
func (b *redesignFixture) GetV2TagSystem(_ context.Context, path string) (json.RawMessage, error) {
	b.tagPath = path
	b.tagCalls++
	return json.RawMessage(`{}`), nil
}
func (b *redesignFixture) MutateV2TagSystem(_ context.Context, method, path string, _ any) (json.RawMessage, error) {
	b.method, b.tagPath = method, path
	b.tagCalls++
	return json.RawMessage(`{}`), nil
}

func serve(s *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRedesignRoutesForwardToPairedWorkerPaths(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	b := &redesignFixture{}
	s := New(context.Background(), startedTracker(), b, &fakeProcessor{}, testLogger(), 1)
	defer s.Drain(time.Second)
	for _, test := range []struct{ method, path, body, want string }{
		{"GET", "/api/collections/review-summary", "", "/api/enrichment/collections/review-summary"},
		{"GET", "/api/collections/" + id + "/rules/candidates?status=ignored", "", "/api/enrichment/collections/" + id + "/rules/candidates?status=ignored"},
	} {
		if w := serve(s, test.method, test.path, test.body, nil); w.Code != 200 || b.path != test.want || w.Header().Get("X-Cairn-Collections") != "2" {
			t.Fatalf("%s: %d %q", test.path, w.Code, b.path)
		}
	}
	if w := serve(s, "GET", "/api/collections/bad/rules/candidates", "", nil); w.Code != 400 {
		t.Fatalf("invalid collection: %d", w.Code)
	}
	for _, test := range []struct{ method, path, body, want string }{
		{"GET", "/api/tag-catalog/usage", "", "/api/v2/tag-catalog/usage"},
		{"POST", "/api/tag-catalog/merges", `{"operation_key":"` + id + `"}`, "/api/v2/tag-catalog/merges"},
		{"GET", "/api/tag-catalog/merges/" + id, "", "/api/v2/tag-catalog/merges/" + id},
		{"POST", "/api/tag-catalog/merges/" + id + "/undo", `{"operation_key":"` + id + `"}`, "/api/v2/tag-catalog/merges/" + id + "/undo"},
	} {
		if w := serve(s, test.method, test.path, test.body, nil); w.Code != 200 || b.tagPath != test.want {
			t.Fatalf("%s: %d %q", test.path, w.Code, b.tagPath)
		}
	}
	before := b.tagCalls
	if w := serve(s, "GET", "/api/tag-catalog/merges/not-a-key", "", nil); w.Code != 400 || b.tagCalls != before {
		t.Fatalf("invalid merge key reached backend: %d", w.Code)
	}
	if w := serve(s, "POST", "/api/tag-catalog/merges", `{}`, nil); w.Code != 400 || b.tagCalls != before {
		t.Fatalf("merge without operation key: %d", w.Code)
	}
}

func TestPreferencesProxyIsExactAndSameOrigin(t *testing.T) {
	const key = "11111111-1111-4111-8111-111111111111"
	b := &redesignFixture{}
	s := New(context.Background(), startedTracker(), b, &fakeProcessor{}, testLogger(), 1)
	defer s.Drain(time.Second)
	for _, test := range []struct{ method, path, body, want string }{
		{"GET", "/api/preferences", "", "/api/enrichment/preferences"},
		{"POST", "/api/preferences/operations", `{"operation_key":"` + key + `","expected_revision":0,"patch":{"ai_suggestion_limit":4}}`, "/api/enrichment/preferences/operations"},
		{"GET", "/api/links/legacy-dropped", "", "/api/enrichment/links/legacy-dropped"},
	} {
		if w := serve(s, test.method, test.path, test.body, nil); w.Code != 200 || b.prefPath != test.want || b.method != test.method {
			t.Fatalf("%s: %d %q", test.path, w.Code, b.prefPath)
		}
	}
	before := b.prefCalls
	for _, test := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{"POST", "/api/preferences/operations", `{"operation_key":"x"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"POST", "/api/preferences/operations", `{"operation_key":"x"}`, map[string]string{"Origin": "https://foreign.invalid"}, http.StatusForbidden},
		{"POST", "/api/preferences/operations", `{"patch":{}}`, nil, http.StatusBadRequest},
		{"GET", "/api/preferences?x=1", "", nil, http.StatusBadRequest},
		{"POST", "/api/preferences/operations", `{"operation_key":"x","pad":"` + strings.Repeat("x", 9000) + `"}`, nil, http.StatusBadRequest},
	} {
		if w := serve(s, test.method, test.path, test.body, test.headers); w.Code != test.status {
			t.Fatalf("%s %v: %d", test.path, test.headers, w.Code)
		}
	}
	if b.prefCalls != before {
		t.Fatal("rejected preference request reached backend")
	}
}

func TestUnifiedTagFilterQuery(t *testing.T) {
	const custom = "11111111-1111-4111-8111-111111111111"
	parse := func(raw string) error {
		_, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?"+raw, nil))
		return err
	}
	q, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?tags=system/topics/ui_design,custom/default/"+custom+",system/topics/ui_design&tags_mode=all&tags_children=1", nil))
	if err != nil || strings.Join(q.Tags, ",") != "system/topics/ui_design,custom/default/"+custom || q.TagsMode != "all" || !q.TagsChildren || !q.NeedsFilterContract() || !q.NeedsTagFilterContract() {
		t.Fatalf("parsed %+v %v", q, err)
	}
	for _, bad := range []string{
		"tags=topics/ui_design", "tags=system/nope/ui_design", "tags=custom/other/" + custom, "tags=custom/default/x",
		"tags=system/topics/UI", "tags_mode=all", "tags=system/topics/a&tags_mode=some", "tags=system/topics/a&tags_children=yes",
		"tags=system/topics/a&tags=system/topics/b", "tags=" + strings.Repeat("system/topics/a,", 33) + "system/topics/b",
	} {
		if parse(bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
