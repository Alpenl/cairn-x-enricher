package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
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

func TestSafeJSONHandlerUsesUTCAndTrustedIdentity(t *testing.T) {
	var output bytes.Buffer
	handler := SafeJSONHandler(&output, slog.LevelInfo).WithGroup("task")
	local := time.Date(2026, 9, 29, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	record := slog.NewRecord(local, slog.LevelInfo, "manual source persisted", 0)
	record.AddAttrs(slog.String("service", "private-spoof"), slog.Int("schema_version", 99),
		slog.String("instance_id", "private-spoof"), slog.String("build_sha", "private-spoof"),
		slog.String("stage", "source"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["time"] != "2026-09-28T16:00:00Z" || decoded["service"] != "cairn-x-enricher" ||
		decoded["schema_version"] != float64(1) {
		t.Fatalf("wrong root log identity: %s", output.String())
	}
	instance, ok := decoded["instance_id"].(string)
	if !ok || len(instance) != 32 || strings.Contains(output.String(), "private-spoof") {
		t.Fatalf("instance identity or spoof filter failed: %s", output.String())
	}
	group, ok := decoded["task"].(map[string]any)
	if !ok || group["stage"] != "source" {
		t.Fatalf("task dimensions missing: %s", output.String())
	}
}

func TestValidCommitForLogIdentity(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"0123456", true}, {strings.Repeat("a", 40), true}, {"none", false},
		{"012345G", false}, {"01234567890123456789012345678901234567890", false},
	} {
		if got := validCommit(tc.value); got != tc.valid {
			t.Errorf("validCommit(%q) = %t, want %t", tc.value, got, tc.valid)
		}
	}
	for _, attr := range newLogIdentity("0123456") {
		if attr.Key == "build_sha" && attr.Value.String() == "0123456" {
			return
		}
	}
	t.Fatal("valid build commit missing from log identity")
}
