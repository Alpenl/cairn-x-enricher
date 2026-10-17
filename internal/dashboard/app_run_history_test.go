package dashboard

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type appHistoryFixture struct {
	*fakeBackend
	calls          int
	id, run, after int64
	err            error
}

func (b *appHistoryFixture) GetAppRunHistory(_ context.Context, id, run, after int64) (json.RawMessage, error) {
	b.calls++
	b.id, b.run, b.after = id, run, after
	return json.RawMessage(`{"version":1,"bookmark_id":4,"run":{"id":9,"resolved_model":null,"usage":{}}}`), b.err
}

func TestAppHistoryIsExplicitReadOnlyAndOptional(t *testing.T) {
	b := &appHistoryFixture{fakeBackend: &fakeBackend{}}
	s := New(context.Background(), nil, b, nil, testLogger(), 1)
	get := func(method, path, ack string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		request.Header.Set("X-Cairn-App-Run-History", ack)
		s.Handler().ServeHTTP(r, request)
		return r
	}
	for _, test := range []struct {
		path       string
		run, after int64
	}{
		{"/api/bookmarks/4/run-history", 0, 0},
		{"/api/bookmarks/4/run-history?after_id=12", 0, 12},
		{"/api/bookmarks/4/run-history/9", 9, 0},
	} {
		r := get("GET", test.path, "1")
		if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("X-Cairn-App-Run-History") != "1" || b.id != 4 || b.run != test.run || b.after != test.after {
			t.Fatalf("lost explicit contract: %d %s", r.Code, r.Body.String())
		}
	}
	for _, path := range []string{
		"/api/bookmarks/4/run-history?after_id=2&after_id=3", "/api/bookmarks/4/run-history?all=1",
		"/api/bookmarks/4/run-history?after_id=0", "/api/bookmarks/4/run-history?after_id=01",
		"/api/bookmarks/4/run-history?after_id=%xx", "/api/bookmarks/4/run-history/9?after_id=3",
		"/api/bookmarks/9007199254740992/run-history", "/api/bookmarks/4/run-history/0",
	} {
		if r := get("GET", path, "1"); r.Code != 400 || b.calls != 3 {
			t.Fatalf("invalid input reached history: %s %d calls=%d", path, r.Code, b.calls)
		}
	}
	for _, method := range []string{"POST", "PATCH", "DELETE", "HEAD"} {
		if r := get(method, "/api/bookmarks/4/run-history/9", "1"); r.Code != 405 || b.calls != 3 {
			t.Fatal("mutation reached stored history")
		}
	}
	if r := get("GET", "/api/bookmarks/4/run-history", ""); r.Code != 409 || b.calls != 3 {
		t.Fatal("history read was not opt-in")
	}
	for _, test := range []struct {
		err    error
		status int
		ack    string
	}{
		{cairn.ErrV2Unsupported, 501, ""}, {cairn.ErrRunHistoryUnavailable, 404, "1"},
	} {
		b.err = test.err
		r := get("GET", "/api/bookmarks/4/run-history/9", "1")
		if r.Code != test.status || r.Header().Get("X-Cairn-App-Run-History") != test.ack {
			t.Fatal("unavailable history and unsupported service were conflated")
		}
	}
	old := New(context.Background(), nil, &fakeBackend{}, nil, testLogger(), 1)
	r := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(context.Background(), "GET", "/api/bookmarks/4/run-history", nil)
	request.Header.Set("X-Cairn-App-Run-History", "1")
	old.Handler().ServeHTTP(r, request)
	if r.Code != 501 || r.Header().Get("X-Cairn-App-Run-History") != "" {
		t.Fatal("old backend pretended to support full history")
	}
}
