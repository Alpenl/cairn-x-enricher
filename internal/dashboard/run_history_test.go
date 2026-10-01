package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type historyFixture struct {
	fakeBackend
	pages, details int
	cursor         int64
}

func (f *historyFixture) GetRunSummaryPage(_ context.Context, _, after int64) (cairn.RunSummaryPage, error) {
	f.pages++
	f.cursor = after
	return cairn.RunSummaryPage{Runs: []cairn.RunSummary{{ID: 12, Status: "succeeded", Archived: true}}}, nil
}
func (f *historyFixture) GetRunDetail(_ context.Context, _, id int64) (cairn.StoredRun, error) {
	f.details++
	return cairn.StoredRun{ID: id, RawJudgments: json.RawMessage(`{"private":"provider source metadata"}`),
		Answers: json.RawMessage(`{"topic":{"accepted":true}}`), Usage: json.RawMessage(`{"tokens":1}`)}, nil
}

func TestRunHistoryOnlyLoadsRequestedPayloadAndOmitsRawMetadata(t *testing.T) {
	backend := &historyFixture{}
	s := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer s.Drain(time.Second)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		return r
	}
	r := get("/api/bookmarks/28/runs?after_id=40")
	if r.Code != 200 || backend.pages != 1 || backend.details != 0 || backend.cursor != 40 {
		t.Fatalf("summary must stay lightweight: %d %s", r.Code, r.Body.String())
	}
	r = get("/api/bookmarks/28/runs/12")
	if r.Code != 200 || backend.details != 1 || !strings.Contains(r.Body.String(), `"answers"`) || strings.Contains(r.Body.String(), "provider source metadata") {
		t.Fatalf("detail contract leaked or omitted payload: %d %s", r.Code, r.Body.String())
	}
	if get("/api/bookmarks/28/runs?after_id=-1").Code != 400 || backend.pages != 1 {
		t.Fatal("invalid cursor reached backend")
	}
}
