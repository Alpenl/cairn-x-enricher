package enrich

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFailureMessageSurvivesTruncation guards the diagnostic quality of the
// failure text that reaches the operator.
//
// A real production record stored "run Eino enrichment workflow:
// [NodeRunError] model API returned HTTP 5". The Worker truncates the stored
// error, and 45 of those 70 characters were framework prefixes, so the status
// code itself was cut in half and 500/502/503 became indistinguishable. Keep
// the status and safe error class within the observed limit, without including
// provider-supplied free text that may echo private source content.
func TestFailureMessageSurvivesTruncation(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`)),
	}
	parsed := readModelHTTPError(resp)

	generator := generatorFunc(func(context.Context, Input) (Candidate, error) {
		return Candidate{}, parsed
	})
	workflow, err := NewWorkflow(generator, testTaxonomy())
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	_, err = workflow.Enrich(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	if err == nil {
		t.Fatal("Enrich() error = nil, want the model failure")
	}

	message := err.Error()
	// The Worker was observed to store roughly 70 characters.
	const workerLimit = 70
	if len(message) > workerLimit {
		t.Fatalf("failure message is %d characters (%q), want <= %d so the stored "+
			"message is not truncated", len(message), message, workerLimit)
	}
	for _, want := range []string{"502", "upstream_error"} {
		if !strings.Contains(message, want) {
			t.Errorf("failure message %q is missing %q", message, want)
		}
	}
	if strings.Contains(message, "temporarily unavailable") {
		t.Errorf("failure message contains provider-supplied free text: %q", message)
	}
	if strings.Contains(message, "NodeRunError") || strings.Contains(message, "Eino") {
		t.Errorf("failure message %q still carries framework noise", message)
	}
}

// TestModelHTTPErrorKeepsTypeAndStatus proves errors.As still exposes the typed
// error after the workflow unwraps it, so retry classification keeps working.
func TestModelHTTPErrorKeepsTypeAndStatus(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"later","type":"api_error"}}`)),
	}
	err := readModelHTTPError(resp)

	var modelErr *ModelHTTPError
	if !errors.As(err, &modelErr) {
		t.Fatalf("readModelHTTPError() = %v, want *ModelHTTPError", err)
	}
	if modelErr.StatusCode != http.StatusServiceUnavailable || modelErr.Type != "api_error" {
		t.Fatalf("parsed error = %+v", modelErr)
	}
	// The unwrapping in Workflow.Enrich must not break errors.As.
	generator := generatorFunc(func(context.Context, Input) (Candidate, error) {
		return Candidate{}, err
	})
	workflow, buildErr := NewWorkflow(generator, testTaxonomy())
	if buildErr != nil {
		t.Fatalf("NewWorkflow() error = %v", buildErr)
	}
	_, enrichErr := workflow.Enrich(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1"})
	if !errors.As(enrichErr, &modelErr) {
		t.Fatalf("Enrich() error = %v, want it to unwrap to *ModelHTTPError", enrichErr)
	}
	if modelErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unwrapped status = %d, want 503", modelErr.StatusCode)
	}
}

// TestFailureMessageWithoutType covers a provider that omits the type field:
// the status must still lead the message rather than producing an empty section.
func TestFailureMessageWithoutType(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`)),
	}
	got := readModelHTTPError(resp).Error()
	if !strings.HasPrefix(got, "HTTP 429") {
		t.Errorf("message = %q, want it to start with the status", got)
	}
	if strings.Contains(got, "slow down") {
		t.Errorf("message = %q, provider free text must be omitted", got)
	}
}

// TestFailureMessageWithEmptyBody covers a gateway that returns 5xx with no JSON
// body, which is what a proxy timeout looks like.
func TestFailureMessageWithEmptyBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusGatewayTimeout,
		Body:       io.NopCloser(strings.NewReader("")),
	}
	got := readModelHTTPError(resp).Error()
	if got != "HTTP 504" {
		t.Errorf("message = %q, want %q", got, "HTTP 504")
	}
}

// TestSafeProviderType allows compact codes without accepting arbitrary echoed
// input or control characters as part of the stored failure message.
func TestSafeProviderType(t *testing.T) {
	long := strings.Repeat("x", 200)
	if got := safeProviderType("  " + long + "  "); got != "" {
		t.Fatalf("safeProviderType() = %q, want empty", got)
	}
	if got := safeProviderType("  upstream_error  "); got != "upstream_error" {
		t.Errorf("safeProviderType() = %q, want it trimmed", got)
	}
	for _, raw := range []string{"secret value", "input\nvalue", "secret:123", "私密原文"} {
		if got := safeProviderType(raw); got != "" {
			t.Errorf("safeProviderType(%q) = %q, want empty", raw, got)
		}
	}
}
