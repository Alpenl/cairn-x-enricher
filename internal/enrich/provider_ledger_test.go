package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type recordingPaidLedger struct {
	mu             sync.Mutex
	reservations   []ProviderAttempt
	settlements    []ProviderSettlement
	settlementErr  error
	authorizations []string
	seen           map[string]bool
}

func (l *recordingPaidLedger) ReserveProviderAttempt(_ context.Context, attempt ProviderAttempt) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservations = append(l.reservations, attempt)
	if l.seen == nil {
		l.seen = make(map[string]bool)
	}
	if l.seen[attempt.OperationKey] {
		return false, nil
	}
	l.seen[attempt.OperationKey] = true
	return true, nil
}
func (l *recordingPaidLedger) SettleProviderAttempt(_ context.Context, settlement ProviderSettlement) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settlements = append(l.settlements, settlement)
	return l.settlementErr
}
func (l *recordingPaidLedger) AuthorizeProviderFallback(_ context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.authorizations = append(l.authorizations, key)
	return nil
}

func TestPaidSourceAttemptIsReservedBeforePOSTAndSettledWithUsage(t *testing.T) {
	ledger := &recordingPaidLedger{}
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		ledger.mu.Lock()
		reserved := len(ledger.reservations) == 1
		ledger.mu.Unlock()
		if !reserved {
			t.Error("provider POST preceded durable reservation")
		}
		posts.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "resp_test_1", "status": "completed", "model": "grok-test",
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120,
				"cost_in_usd_ticks": 1234, "server_side_tool_usage_details": map[string]any{"x_search_calls": 1}},
			"output": []any{map[string]any{"type": "x_search_call", "status": "completed"},
				map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text",
					"text": `{"original_text":"saved source","original_language":"en","context_text":"","related_links":[],"image_urls":[]}`}}}},
		})
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", "grok-test", 1024, "", server.Client(), testTaxonomy())
	client.SetPaidAttemptLedger(ledger)
	var diagnostic bytes.Buffer
	client.SetLogger(slog.New(slog.NewJSONHandler(&diagnostic, nil)))
	input := Input{ID: 7, URL: "https://x.com/a/status/7", LeaseToken: "lease-7",
		ContentRevision: 3, MinRemainingMS: 210_000}
	source, err := client.FetchSource(context.Background(), input)
	if err != nil || source.OriginalText != "saved source" {
		t.Fatalf("source=%+v error=%v", source, err)
	}
	if posts.Load() != 1 || len(ledger.reservations) != 1 || len(ledger.settlements) != 1 {
		t.Fatalf("posts=%d reserve=%d settle=%d", posts.Load(), len(ledger.reservations), len(ledger.settlements))
	}
	a := ledger.reservations[0]
	s := ledger.settlements[0]
	if a.Stage != "fetch" || a.Variant != "fetch_thread" || a.AttemptNumber != 1 ||
		a.LinkID != input.ID || a.ContentRevision != input.ContentRevision || len(a.RequestHash) != 64 ||
		len(a.OperationKey) != 64 || s.OperationKey != a.OperationKey || s.ResponseID == nil ||
		*s.ResponseID != "resp_test_1" || s.CostUSDTicks == nil || *s.CostUSDTicks != 1234 ||
		s.XSearchCalls == nil || *s.XSearchCalls != 1 {
		t.Fatalf("reservation=%+v settlement=%+v", a, s)
	}
	if log := diagnostic.String(); !strings.Contains(log, `"event_name":"provider_attempt_reserved"`) ||
		!strings.Contains(log, `"event_name":"provider_attempt_dispatching"`) ||
		!strings.Contains(log, `"event_name":"provider_response_headers_received"`) ||
		!strings.Contains(log, `"event_name":"provider_attempt_responded"`) ||
		!strings.Contains(log, `"cost_usd_ticks":1234`) ||
		strings.Contains(log, input.LeaseToken) || strings.Contains(log, input.URL) ||
		strings.Contains(log, a.OperationKey) || strings.Contains(log, "resp_test_1") {
		t.Fatalf("paid diagnostic event was incomplete or leaked identity: %s", log)
	}
	log := diagnostic.String()
	for _, pair := range [][2]string{
		{`"event_name":"provider_attempt_reserved"`, `"event_name":"provider_attempt_dispatching"`},
		{`"event_name":"provider_attempt_dispatching"`, `"event_name":"provider_response_headers_received"`},
		{`"event_name":"provider_response_headers_received"`, `"event_name":"provider_attempt_responded"`},
	} {
		if strings.Index(log, pair[0]) >= strings.Index(log, pair[1]) {
			t.Fatalf("provider event order is wrong: %s", log)
		}
	}
	// Re-entering the same leased operation cannot obtain a second permit.
	_, err = client.FetchSource(context.Background(), input)
	if err == nil || posts.Load() != 1 {
		t.Fatalf("replay posts=%d error=%v", posts.Load(), err)
	}
}

func TestLostProviderResponseLeavesPaidAttemptUnsettledAndStopsFallback(t *testing.T) {
	ledger := &recordingPaidLedger{}
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", "grok-test", 1024, "", server.Client(), testTaxonomy())
	client.SetPaidAttemptLedger(ledger)
	var diagnostic bytes.Buffer
	client.SetLogger(slog.New(slog.NewJSONHandler(&diagnostic, nil)))
	_, err := client.FetchSource(context.Background(), Input{ID: 8, URL: "https://x.com/a/status/8",
		LeaseToken: "lease-8", ContentRevision: 1, MinRemainingMS: 210_000})
	if err == nil || posts.Load() != 1 || len(ledger.reservations) != 1 ||
		len(ledger.settlements) != 0 || len(ledger.authorizations) != 0 {
		t.Fatalf("posts=%d reserve=%d settle=%d fallback=%d error=%v", posts.Load(),
			len(ledger.reservations), len(ledger.settlements), len(ledger.authorizations), err)
	}
	if !strings.Contains(diagnostic.String(), `"event_name":"provider_attempt_dispatching"`) ||
		strings.Contains(diagnostic.String(), `"event_name":"provider_response_headers_received"`) ||
		!strings.Contains(diagnostic.String(), `"event_name":"provider_attempt_unknown"`) ||
		!strings.Contains(diagnostic.String(), `"provider_reason":"network_unknown"`) {
		t.Fatalf("unknown attempt event missing: %s", diagnostic.String())
	}
}

func TestResponseHeadersAreLoggedBeforeFailedSettlement(t *testing.T) {
	ledger := &recordingPaidLedger{settlementErr: errors.New("private settlement failure")}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", "grok-test", 1024, "", server.Client(), testTaxonomy())
	client.SetPaidAttemptLedger(ledger)
	var diagnostic bytes.Buffer
	client.SetLogger(slog.New(slog.NewJSONHandler(&diagnostic, nil)))
	_, err := client.Transform(context.Background(), Input{ID: 10, SourceText: "source", LeaseToken: "lease-10",
		ContentRevision: 1, MinRemainingMS: 210_000})
	if err == nil || len(ledger.settlements) != 1 {
		t.Fatalf("settlement=%d error=%v", len(ledger.settlements), err)
	}
	log := diagnostic.String()
	if !strings.Contains(log, `"event_name":"provider_response_headers_received"`) ||
		!strings.Contains(log, `"provider_http_status":503`) ||
		!strings.Contains(log, `"provider_reason":"settlement_failed"`) ||
		strings.Contains(log, `"event_name":"provider_attempt_responded"`) ||
		strings.Contains(log, "private settlement failure") {
		t.Fatalf("settlement failure event is inaccurate or leaks private error: %s", log)
	}
}

func TestPaidCallFailsClosedWhenLedgerDeniesOrIdentityMissing(t *testing.T) {
	ledger := &recordingPaidLedger{seen: map[string]bool{}}
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", "grok-test", 1024, "", server.Client(), testTaxonomy())
	client.SetPaidAttemptLedger(ledger)
	_, err := client.Transform(context.Background(), Input{ID: 9, SourceText: "source"})
	if err == nil || posts.Load() != 0 {
		t.Fatalf("missing lease posts=%d error=%v", posts.Load(), err)
	}
	input := Input{ID: 9, LeaseToken: "lease-9", ContentRevision: 1, MinRemainingMS: 210_000, SourceText: "source"}
	_, err = client.Transform(context.Background(), input)
	if err == nil {
		t.Fatal("expected malformed provider response")
	}
	_, err = client.Transform(context.Background(), input)
	if err == nil || posts.Load() != 1 {
		t.Fatalf("duplicate posts=%d error=%v", posts.Load(), err)
	}
}
