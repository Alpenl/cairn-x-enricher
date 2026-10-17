package dashboard

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type savedRecordsFixture struct {
	*fakeBackend
	calls int
	err   error
	query cairn.SavedRecordQuery
}

func (b *savedRecordsFixture) GetSavedRecords(_ context.Context, _ int64, q cairn.SavedRecordQuery) (json.RawMessage, error) {
	b.calls++
	b.query = q
	return json.RawMessage(`{"version":1}`), b.err
}

func TestSavedRecordsAllowlistOptionalCapabilityAndReadonly(t *testing.T) {
	b := &savedRecordsFixture{fakeBackend: &fakeBackend{}}
	s := New(context.Background(), nil, b, nil, testLogger(), 1)
	read := func(method, path, ack string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		r.Header.Set("X-Cairn-App-Saved-Records", ack)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	base := "/api/bookmarks/4/saved-records/"
	for _, path := range []string{"sources", "overrides?record_key=9", "events?after_key=8", "entities?source_revision=2&record_key=opaque%2F%2B"} {
		w := read("GET", base+path, "1")
		if w.Code != 200 || w.Header().Get("X-Cairn-App-Saved-Records") != "1" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("saved history lost opt-in/uncached contract")
		}
	}
	if b.query.RecordKey != "opaque/+" || b.query.SourceRevision != 2 {
		t.Fatal("opaque identity changed")
	}
	for _, path := range []string{"unknown", "entities", "sources?source_revision=1", "sources?record_key=1&after_key=2", "sources?record_key=", "events?after_key=01", "events?after_key=1&after_key=2", "events?bad=1", "events?after_key=%xx"} {
		if w := read("GET", base+path, "1"); w.Code != 400 || b.calls != 4 {
			t.Fatalf("bad scope reached backend: %s %d", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "PATCH", "DELETE", "HEAD"} {
		if w := read(method, base+"sources", "1"); w.Code != 405 || b.calls != 4 {
			t.Fatal("history mutation allowed")
		}
	}
	if w := read("GET", base+"sources", ""); w.Code != 409 || b.calls != 4 {
		t.Fatal("read was not explicit")
	}
	for _, test := range []struct {
		err    error
		status int
	}{{cairn.ErrV2Unsupported, 501}, {cairn.ErrRunHistoryUnavailable, 404}} {
		b.err = test.err
		if w := read("GET", base+"sources", "1"); w.Code != test.status {
			t.Fatal("lost history error semantics")
		}
	}
	old := New(context.Background(), nil, &fakeBackend{}, nil, testLogger(), 1)
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), "GET", base+"sources", nil)
	r.Header.Set("X-Cairn-App-Saved-Records", "1")
	old.Handler().ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatal("old backend fabricated support")
	}
}
