package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type recoveryProcessor struct {
	fakeProcessor
	calls    int
	accepted bool
}

func (p *recoveryProcessor) SourceRecoveryStatus(context.Context) (cairn.ProviderCheckStatus, error) {
	return cairn.ProviderCheckStatus{State: "waiting", LeaseToken: "private"}, nil
}
func (p *recoveryProcessor) RecoverSource(context.Context) (cairn.ProviderCheckStatus, error) {
	p.calls++
	if !p.accepted {
		return cairn.ProviderCheckStatus{State: "waiting"}, nil
	}
	return cairn.ProviderCheckStatus{State: "pending", Accepted: p.accepted, LeaseToken: "private"}, nil
}
func TestRecoveryEndpointProtectsAdmissionAndWakesOnlyAcceptedRequests(t *testing.T) {
	p := &recoveryProcessor{accepted: true}
	s := New(context.Background(), startedTracker(), &fakeBackend{}, p, testLogger(), 1)
	defer s.Drain(time.Second)
	wake := make(chan struct{}, 1)
	s.SetWakeup(wake)
	for _, test := range []struct {
		origin, content, body string
		code                  int
	}{
		{"https://evil.test", "application/json", "{}", 403},
		{"", "text/plain", "{}", 400}, {"", "application/json", "{}{}", 400},
		{"", "application/json", "null", 400}, {"", "application/json", `{"force":true}`, 400},
		{"http://example.com", "application/json", "{}", 202},
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/api/service/recover", strings.NewReader(test.body))
		req.Header.Set("Content-Type", test.content)
		req.Header.Set("Origin", test.origin)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		if response.Code != test.code || strings.Contains(response.Body.String(), "private") {
			t.Fatalf("response %d %s", response.Code, response.Body.String())
		}
	}
	if p.calls != 1 || len(wake) != 1 {
		t.Fatalf("admission calls=%d wakes=%d", p.calls, len(wake))
	}
	<-wake
	p.accepted = false
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/service/recover", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if len(wake) != 0 {
		t.Fatal("denied recovery woke scheduler")
	}
}
