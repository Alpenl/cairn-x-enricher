package observability

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestSafeJSONHandlerFiltersMessagesFieldsAndGroups(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(SafeJSONHandler(&output, slog.LevelInfo))
	private := "https://example.test/private?token=source-secret"
	logger.WithGroup(private).With("link_id", 42, "component", "source").Info(private,
		"request_id", "123e4567-e89b-12d3-a456-426614174000", "error", errors.New(private))
	logger.Info("manual source persisted", "stage", "source", "attempt", 2)
	got := output.String()
	for _, want := range []string{`"msg":"application_event"`, `"group":{"component":"source"`,
		`"msg":"manual source persisted"`, `"stage":"source"`, `"attempt":2`} {
		if !strings.Contains(got, want) {
			t.Fatalf("safe field %q missing: %s", want, got)
		}
	}
	for _, secret := range []string{private, "source-secret", "link_id", "request_id",
		"123e4567-e89b-12d3-a456-426614174000"} {
		if strings.Contains(got, secret) {
			t.Fatalf("private field %q leaked: %s", secret, got)
		}
	}
}

func TestSafeJSONHandlerKeepsProviderTransportBoundaries(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(SafeJSONHandler(&output, slog.LevelInfo))
	logger.Info("provider attempt", "event_name", "provider_attempt_dispatching", "stage", "reading",
		"operation_key", "private-operation")
	logger.Info("provider attempt", "event_name", "provider_response_headers_received", "stage", "reading",
		"provider_http_status", 503, "response_id", "private-response")
	got := output.String()
	for _, want := range []string{`"event_name":"provider_attempt_dispatching"`,
		`"event_name":"provider_response_headers_received"`, `"provider_http_status":503`} {
		if !strings.Contains(got, want) {
			t.Fatalf("provider boundary field %q missing: %s", want, got)
		}
	}
	for _, private := range []string{"private-operation", "private-response", "operation_key", "response_id"} {
		if strings.Contains(got, private) {
			t.Fatalf("private provider field %q leaked: %s", private, got)
		}
	}
}
