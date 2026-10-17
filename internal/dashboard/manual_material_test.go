package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type manualMaterialFixture struct {
	*fakeBackend
	reads, writes int
	intent        cairn.ManualMaterialIntent
	replayed      bool
	err           error
}

func (b *manualMaterialFixture) GetManualMaterial(_ context.Context, id int64) (json.RawMessage, error) {
	b.reads++
	if id != 4 {
		return nil, errors.New("foreign bookmark")
	}
	return json.RawMessage(`{"version":1,"id":4,"can_submit":false,"blocked_reason":"no_observed_gap"}`), b.err
}
func (b *manualMaterialFixture) SubmitManualMaterial(_ context.Context, id int64, intent cairn.ManualMaterialIntent) (json.RawMessage, error) {
	b.writes++
	b.intent = intent
	if id != 4 {
		return nil, errors.New("foreign bookmark")
	}
	if b.replayed {
		return json.RawMessage(`{"classification_queued":true,"replayed":true}`), b.err
	}
	return json.RawMessage(`{"classification_queued":true,"replayed":false}`), b.err
}
func dashboardMaterialIntent() cairn.ManualMaterialIntent {
	return cairn.ManualMaterialIntent{Version: 1, OperationKey: "persistent-intent", ExpectedContentRevision: 3, EvidenceSnapshotID: 9,
		SourceHash: strings.Repeat("a", 64), TargetGeneration: 2, Kind: "truncation", SourceRef: "primary", Text: "Manual continuation"}
}

func TestManualMaterialGatewayPreservesIntentAndSeparatesReadAndPaidFollowup(t *testing.T) {
	b := &manualMaterialFixture{fakeBackend: &fakeBackend{}}
	source, classification := make(chan struct{}, 2), make(chan struct{}, 2)
	s := New(context.Background(), nil, b, nil, testLogger(), 1)
	s.SetWakeup(source)
	s.SetClassificationWakeup(classification)
	send := func(method, path, ack, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		request.Header.Set("X-Cairn-App-Manual-Material", ack)
		s.Handler().ServeHTTP(r, request)
		return r
	}
	path := "/api/bookmarks/4/manual-material"
	r := send("GET", path, "1", "")
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("X-Cairn-App-Manual-Material") != "1" || b.reads != 1 || b.writes != 0 || len(source)+len(classification) != 0 {
		t.Fatal("ordinary read initiated processing")
	}
	body, _ := json.Marshal(dashboardMaterialIntent())
	r = send("POST", path, "1", string(body))
	if r.Code != 200 || b.intent != dashboardMaterialIntent() || b.writes != 1 || len(source) != 0 || len(classification) != 1 {
		t.Fatal("write changed intent or queued reading")
	}
	b.replayed = true
	if r = send("POST", path, "1", string(body)); r.Code != 200 || b.writes != 2 || len(classification) != 1 {
		t.Fatal("receipt replay enqueued new work")
	}
	beforeReads, beforeWrites := b.reads, b.writes
	for _, test := range []struct {
		method, path, ack, body string
		status                  int
	}{
		{"GET", path, "", "", 409}, {"POST", path, "", string(body), 409}, {"DELETE", path, "1", "", 405}, {"HEAD", path, "1", "", 405},
		{"GET", path + "?model=other", "1", "", 400}, {"GET", "/api/bookmarks/04/manual-material", "1", "", 400},
		{"GET", "/api/bookmarks/9007199254740992/manual-material", "1", "", 400},
		{"POST", path, "1", string(body) + "{}", 400}, {"POST", path, "1", strings.Replace(string(body), `"version":1`, `"version":2`, 1), 400},
		{"POST", path, "1", strings.Replace(string(body), `"target_generation":2,`, "", 1), 400},
		{"POST", path, "1", strings.TrimSuffix(string(body), "}") + `,"model":"other"}`, 400},
		{"POST", path, "1", strings.Repeat(" ", 129<<10), 400},
		{"POST", path, "1", strings.Replace(string(body), "Manual continuation", "bad\xff", 1), 400},
	} {
		if r := send(test.method, test.path, test.ack, test.body); r.Code != test.status {
			t.Fatalf("expected %d got %d: %s", test.status, r.Code, r.Body.String())
		}
	}
	if b.reads != beforeReads || b.writes != beforeWrites {
		t.Fatal("invalid request reached backend")
	}
	for _, status := range []int{400, 404, 409, 413, 503} {
		b.err = &cairn.APIError{StatusCode: status, Code: "provider_result_unknown"}
		r := send("POST", path, "1", string(body))
		if r.Code != status || !strings.Contains(r.Body.String(), "provider_result_unknown") || len(classification) != 1 {
			t.Fatal("conflict was hidden or paid followup queued")
		}
	}
	b.err = cairn.ErrV2Unsupported
	if r := send("GET", path, "1", ""); r.Code != 501 || r.Header().Get("X-Cairn-App-Manual-Material") != "" {
		t.Fatal("old backend falsely advertised capability")
	}
	b.err = cairn.ErrInvalidManualMaterial
	if r := send("POST", path, "1", string(body)); r.Code != 400 || r.Header().Get("X-Cairn-App-Manual-Material") != "1" || len(classification) != 1 {
		t.Fatal("known pre-send overflow became an unknown write or woke the queue")
	}
	old := New(context.Background(), nil, &fakeBackend{}, nil, testLogger(), 1)
	request := httptest.NewRequestWithContext(context.Background(), "GET", path, nil)
	request.Header.Set("X-Cairn-App-Manual-Material", "1")
	r = httptest.NewRecorder()
	old.Handler().ServeHTTP(r, request)
	if r.Code != 501 {
		t.Fatal("missing backend capability was not respected")
	}
}
