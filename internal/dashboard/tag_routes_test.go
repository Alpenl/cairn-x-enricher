package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type tagProxyCall struct {
	method, path string
	body         any
}
type tagProxyBackend struct {
	*fakeBackend
	calls   []tagProxyCall
	payload json.RawMessage
	err     error
}

func (backend *tagProxyBackend) GetV2TagSystem(_ context.Context, path string) (json.RawMessage, error) {
	backend.calls = append(backend.calls, tagProxyCall{method: http.MethodGet, path: path})
	return backend.payload, backend.err
}
func (backend *tagProxyBackend) MutateV2TagSystem(_ context.Context, method, path string, body any) (json.RawMessage, error) {
	backend.calls = append(backend.calls, tagProxyCall{method: method, path: path, body: body})
	return backend.payload, backend.err
}
func TestTagSystemProxyRoutesAndIncrementalBody(t *testing.T) {
	backend := &tagProxyBackend{fakeBackend: &fakeBackend{}, payload: json.RawMessage(`{"revision":5,"selection":{"topics":["image_creation"],"resource_kinds":["skill"]},"custom_tags":[]}`)}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for _, test := range []struct{ method, path, upstream, body string }{
		{"GET", "/api/bookmarks/4/tags", "/api/v2/links/4/tags", ""},
		{"GET", "/api/bookmarks/4/tag-history?limit=30&before_id=9", "/api/v2/links/4/tag-history?before_id=9&limit=30", ""},
		{"GET", "/api/custom-tags", "/api/v2/custom-tags", ""},
		{"POST", "/api/bookmarks/4/tags", "/api/v2/links/4/tags", `{"operation_key":"op-replace","expected_revision":4,"expected_decision_id":3,"expected_content_revision":2,"actions":[{"action":"replace","from_tag_ref":"system/topics/ai_coding","to_tag_ref":"system/topics/image_creation"}]}`},
		{"PATCH", "/api/custom-tags/custom-1", "/api/v2/custom-tags/custom-1", `{"operation_key":"rename-1","expected_revision":2,"label":"项目资料"}`},
		{"DELETE", "/api/custom-tags/custom-1", "/api/v2/custom-tags/custom-1", `{"operation_key":"archive-1","expected_revision":2,"detach_all":true}`},
		{"GET", "/api/tag-counts?resource_kinds=skill&custom_tags=custom-1&custom_mode=all", "/api/v2/tags/counts?custom_mode=all&custom_tags=custom-1&resource_kinds=skill", ""},
	} {
		request := httptest.NewRequestWithContext(context.Background(), test.method, test.path, strings.NewReader(test.body))
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
		call := backend.calls[len(backend.calls)-1]
		if call.method != test.method || call.path != test.upstream {
			t.Fatalf("unexpected forwarded call: %+v", call)
		}
		if test.body != "" {
			encoded, _ := json.Marshal(call.body)
			var want, got any
			_ = json.Unmarshal([]byte(test.body), &want)
			_ = json.Unmarshal(encoded, &got)
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Fatalf("mutation changed: %s", encoded)
			}
		}
	}
}
func TestTagSystemUnsupportedDoesNotPretendEmpty(t *testing.T) {
	backend := &tagProxyBackend{fakeBackend: &fakeBackend{}, err: cairn.ErrV2Unsupported}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks/4/tags", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("unsupported tags: %d %s", response.Code, response.Body.String())
	}
	backend.err = &cairn.APIError{StatusCode: 409, Code: "revision_conflict", Revision: ptrInt64(9)}
	request := httptest.NewRequestWithContext(context.Background(), "POST", "/api/bookmarks/4/tags", strings.NewReader(`{"operation_key":"safe-retry","expected_revision":8,"actions":[{"action":"reject","tag_ref":"system/topics/ai_coding"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"revision":9`) {
		t.Fatalf("lost CAS conflict: %d %s", response.Code, response.Body.String())
	}
}
func ptrInt64(value int64) *int64 { return &value }
func TestTagSystemRejectsMalformedRequestsBeforeForwarding(t *testing.T) {
	backend := &tagProxyBackend{fakeBackend: &fakeBackend{}, err: errors.New("must not be called")}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for _, body := range []string{`{"actions":[]}`, `{"operation_key":""}`, `{"operation_key":"op"} {}`} {
		request := httptest.NewRequestWithContext(context.Background(), "POST", "/api/bookmarks/4/tags", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("malformed body %s: %d", body, response.Code)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("invalid body reached backend: %+v", backend.calls)
	}
}
func TestTagFiltersPreserveModesAndIDs(t *testing.T) {
	query, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?topics=image_creation,video_creation&resource_kinds=skill,prompt&custom_tags=123e4567-e89b-12d3-a456-426614174000&topics_mode=all&resource_mode=any&custom_mode=all&filter_contract_version=1", nil))
	if err != nil || len(query.ResourceKinds) != 2 || len(query.CustomTags) != 1 || query.TopicMode != "all" || query.ResourceMode != "any" || query.CustomMode != "all" || !query.RequireEffectiveFilters {
		t.Fatalf("tag query: %+v %v", query, err)
	}
	if _, err := bookmarkQuery(httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks?resource_mode=unknown", nil)); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
