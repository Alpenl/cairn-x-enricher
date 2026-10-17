package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type processingFixture struct {
	*fakeBackend
	reads, writes int
	intent        cairn.ProcessingIntent
	replayed      bool
	err           error
}

func (b *processingFixture) GetProcessing(_ context.Context, _ int64) (json.RawMessage, error) {
	b.reads++
	return json.RawMessage(`{"version":1,"id":4,"model_calls":0}`), b.err
}
func (b *processingFixture) SubmitProcessing(_ context.Context, _ int64, intent cairn.ProcessingIntent) (json.RawMessage, error) {
	b.writes++
	b.intent = intent
	return json.RawMessage(fmt.Sprintf(`{"classification_queued":%t,"reading_queued":%t,"replayed":%t}`,
		intent.Action != "retry_reading", intent.Action != "retry_classification", b.replayed)), b.err
}

func TestProcessingGatewayKeepsActionsAndWakeupsIndependent(t *testing.T) {
	b := &processingFixture{fakeBackend: &fakeBackend{}}
	s := New(t.Context(), nil, b, nil, testLogger(), 1)
	reading, classification := make(chan struct{}, 4), make(chan struct{}, 4)
	s.SetWakeup(reading)
	s.SetClassificationWakeup(classification)
	send := func(method, path, ack, body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		request.Header.Set(cairn.ProcessingCapability, ack)
		s.Handler().ServeHTTP(r, request)
		return r
	}
	path := "/api/bookmarks/4/processing"
	if r := send("GET", path, "1", ""); r.Code != 200 || b.reads != 1 || b.writes != 0 || len(reading)+len(classification) != 0 {
		t.Fatal("read triggered a processing action")
	}
	intent := cairn.ProcessingIntent{Version: 1, OperationKey: "persistent-operation", Fence: strings.Repeat("a", 64),
		Action: "retry_reading", ExpectedContentRevision: 3, TargetGeneration: 0}
	body, _ := json.Marshal(intent)
	if r := send("POST", path, "1", string(body)); r.Code != 200 || b.intent != intent || len(reading) != 1 || len(classification) != 0 {
		t.Fatal("reading retry changed the classification queue")
	}
	b.replayed = true
	if r := send("POST", path, "1", string(body)); r.Code != 200 || len(reading) != 1 || len(classification) != 0 {
		t.Fatal("receipt recovery created a fresh wakeup")
	}
	b.replayed = false
	intent.Action = "retry_classification"
	body, _ = json.Marshal(intent)
	if r := send("POST", path, "1", string(body)); r.Code != 200 || len(reading) != 1 || len(classification) != 1 {
		t.Fatal("classification retry started reading")
	}
	reads, writes := b.reads, b.writes
	for _, tc := range []struct {
		method, path, ack, body string
		status                  int
	}{
		{"GET", path, "", "", 409}, {"GET", path + "?model=other", "1", "", 400},
		{"GET", "/api/bookmarks/04/processing", "1", "", 400}, {"HEAD", path, "1", "", 405}, {"DELETE", path, "1", "", 405},
		{"POST", path, "1", string(body) + "{}", 400}, {"POST", path, "1", strings.Replace(string(body), `"target_generation":0`, `"target_generation":null`, 1), 400},
		{"POST", path, "1", strings.Replace(string(body), `,"target_generation":0`, "", 1), 400},
		{"POST", path, "1", strings.TrimSuffix(string(body), "}") + `,"text":null}`, 400},
		{"POST", path, "1", strings.Repeat(" ", 129<<10), 400},
	} {
		if r := send(tc.method, tc.path, tc.ack, tc.body); r.Code != tc.status {
			t.Fatalf("expected %d, got %d (%s)", tc.status, r.Code, r.Body.String())
		}
	}
	if b.reads != reads || b.writes != writes || len(reading)+len(classification) != 2 {
		t.Fatal("invalid request reached the backend")
	}
	for _, status := range []int{400, 404, 409, 413} {
		b.err = &cairn.APIError{StatusCode: status, Code: "state_conflict"}
		r := send("POST", path, "1", string(body))
		if r.Code != status || r.Header().Get(cairn.ProcessingCapability) != "1" || len(reading)+len(classification) != 2 {
			t.Fatal("known refusal was hidden or woke a paid stage")
		}
	}
	b.err = errors.New("lost response after commit")
	if r := send("POST", path, "1", string(body)); r.Code < 500 || r.Header().Get(cairn.ProcessingCapability) != "" || len(reading)+len(classification) != 2 {
		t.Fatal("unknown write became a known refusal")
	}
	b.err = cairn.ErrV2Unsupported
	if r := send("GET", path, "1", ""); r.Code != 501 || r.Header().Get(cairn.ProcessingCapability) != "" {
		t.Fatal("unsupported backend advertised processing")
	}
	old := New(t.Context(), nil, &fakeBackend{}, nil, testLogger(), 1)
	r := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
	req.Header.Set(cairn.ProcessingCapability, "1")
	old.Handler().ServeHTTP(r, req)
	if r.Code != 501 {
		t.Fatal("old backend gained unsupported operations")
	}
}
