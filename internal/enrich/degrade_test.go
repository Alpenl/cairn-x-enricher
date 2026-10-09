package enrich

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestURLOnlyGenerateNeverCallsModelOrReservesBudget(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	client := NewResponsesClient(server.URL, "fixture", "grok", 1024, "", server.Client(), testTaxonomy())
	ledger := &recordingPaidLedger{}
	client.SetPaidAttemptLedger(ledger)
	for _, text := range []string{"", " \n\t "} {
		_, err := client.Generate(context.Background(), Input{URL: "https://x.com/u/status/1", SourceText: text})
		if !errors.Is(err, ErrCaptureRequired) {
			t.Fatalf("URL-only input: %v", err)
		}
	}
	if calls != 0 || len(ledger.reservations) != 0 {
		t.Fatalf("calls=%d reservations=%d", calls, len(ledger.reservations))
	}
}
