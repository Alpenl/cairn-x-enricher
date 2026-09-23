package enrich

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

// TestModelErrorClassificationSeparatesOperatorResponses pins B02-T06: each
// failure kind has a distinct class so a configuration or contract fault does
// not consume the attempt budget of every queued job, and a stale/conflict
// response is not reported as a semantic model failure.
func TestModelErrorClassificationSeparatesOperatorResponses(t *testing.T) {
	cases := []struct {
		status int
		class  ErrorClass
	}{
		{http.StatusUnauthorized, ErrorClassConfiguration},
		{http.StatusForbidden, ErrorClassConfiguration},
		{http.StatusPaymentRequired, ErrorClassConfiguration},
		{http.StatusBadRequest, ErrorClassContract},
		{http.StatusUnprocessableEntity, ErrorClassContract},
		{http.StatusConflict, ErrorClassStale},
		{http.StatusRequestTimeout, ErrorClassTransient},
		{http.StatusTooManyRequests, ErrorClassTransient},
		{http.StatusInternalServerError, ErrorClassTransient},
		{http.StatusBadGateway, ErrorClassTransient},
		{http.StatusServiceUnavailable, ErrorClassTransient},
		{http.StatusGatewayTimeout, ErrorClassTransient},
	}
	for _, testCase := range cases {
		err := ClassifyModelError(&ModelHTTPError{StatusCode: testCase.status})
		if got := ClassOf(err); got != testCase.class {
			t.Errorf("HTTP %d classified as %q, want %q", testCase.status, got, testCase.class)
		}
		if testCase.class != ErrorClassTransient && IsRetryable(err) {
			t.Errorf("HTTP %d must not be retried in place", testCase.status)
		}
	}
}

func TestConfigurationAndContractPauseTheComponent(t *testing.T) {
	if !PausesComponent(ClassifyModelError(&ModelHTTPError{StatusCode: http.StatusUnauthorized})) {
		t.Fatal("auth failure must pause the component")
	}
	if !PausesComponent(ClassifyModelError(&ModelHTTPError{StatusCode: http.StatusBadRequest})) {
		t.Fatal("contract failure must pause the component")
	}
	if PausesComponent(ClassifyModelError(&ModelHTTPError{StatusCode: http.StatusTooManyRequests})) {
		t.Fatal("rate limit must not pause the component")
	}
}

func TestStaleIsNotAJobFailure(t *testing.T) {
	err := ClassifyModelError(&ModelHTTPError{StatusCode: http.StatusConflict})
	if !IsStale(err) || IsRetryable(err) {
		t.Fatalf("conflict class = %q, retryable=%v", ClassOf(err), IsRetryable(err))
	}
}

func TestTransportFaultsAreTransient(t *testing.T) {
	if !IsRetryable(ClassifyModelError(&net.OpError{Op: "dial", Err: errors.New("connection refused")})) {
		t.Fatal("dial failure must be transient")
	}
	if !IsRetryable(ClassifyModelError(errors.New("Post \"https://api\": server closed connection"))) {
		t.Fatal("closed connection must be transient")
	}
}
