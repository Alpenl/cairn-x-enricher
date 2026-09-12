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
// code itself was cut in half and 500/502/503 became indistinguishable. This
// test pins the fix: the message must lead with the status and the provider's
// error class, and it must stay under the observed storage limit intact.
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
	for _, want := range []string{"502", "upstream_error", "temporarily unavailable"} {
		if !strings.Contains(message, want) {
			t.Errorf("failure message %q is missing %q", message, want)
		}
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
	if !strings.Contains(got, "slow down") {
		t.Errorf("message = %q, want it to keep the provider reason", got)
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

// TestBoundedFieldTrimsProviderValue keeps a hostile or oversized provider field
// from dominating the stored message.
func TestBoundedFieldTrimsProviderValue(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := boundedField("  "+long+"  ", 60)
	if len(got) != 60 {
		t.Fatalf("boundedField() length = %d, want 60", len(got))
	}
	if got := boundedField("  upstream_error  ", 60); got != "upstream_error" {
		t.Errorf("boundedField() = %q, want it trimmed", got)
	}
}
