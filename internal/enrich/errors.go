package enrich

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

// ErrorClass separates failure kinds that need different operator responses.
// The distinction matters because a configuration or contract fault should
// pause the affected component instead of burning every queued job's attempt
// budget, while a transient transport fault is worth a bounded retry.
type ErrorClass string

const (
	// ErrorClassConfiguration covers missing or rejected credentials and
	// endpoints. Retrying cannot help; the component should stop claiming work.
	ErrorClassConfiguration ErrorClass = "configuration"
	// ErrorClassContract covers malformed requests and responses that violate
	// the agreed shape. Retrying with different wording cannot help either.
	ErrorClassContract ErrorClass = "contract"
	// ErrorClassTransient covers timeouts, rate limits, overload and network
	// faults. These are worth a single bounded, backed-off retry.
	ErrorClassTransient ErrorClass = "transient"
	// ErrorClassStale covers an expired lease, a superseded target or input that
	// changed underneath the job. It is not a semantic model failure and must
	// not be counted as one.
	ErrorClassStale ErrorClass = "stale"
	// ErrorClassCompleted reports that the bookmark is already completed. It
	// does not confirm the caller's operation; only its exact idempotent replay
	// may do so. Keep this class for compatibility with older Worker errors.
	ErrorClassCompleted ErrorClass = "already_completed"
	// ErrorClassBudget pauses admission without treating exhaustion as a model failure.
	ErrorClassBudget ErrorClass = "budget"
	// ErrorClassUnknown is the default when nothing else applies. It is treated
	// conservatively as a job-level failure, never as a component pause.
	ErrorClassUnknown ErrorClass = "unknown"
)

// ClassifiedError attaches a class to a cause so callers can decide between a
// bounded retry, a component pause and a plain job failure.
type ClassifiedError struct {
	Class ErrorClass
	Cause error
}

func (e *ClassifiedError) Error() string {
	if e.Cause == nil {
		return string(e.Class)
	}
	return string(e.Class) + ": " + e.Cause.Error()
}

func (e *ClassifiedError) Unwrap() error { return e.Cause }

// classify wraps a cause in a class without nesting an existing classification.
func classify(class ErrorClass, cause error) error {
	if cause == nil {
		return nil
	}
	var existing *ClassifiedError
	if errors.As(cause, &existing) {
		return cause
	}
	return &ClassifiedError{Class: class, Cause: cause}
}

// Classified exposes classify to other packages that need to attach a runtime
// class to an error they produced.
func Classified(cause error, class ErrorClass) error { return classify(class, cause) }

// ClassOf reports the error class, defaulting to unknown. Context cancellation
// is reported as stale: a cancelled job was not a semantic model failure.
func ClassOf(err error) ErrorClass {
	if err == nil {
		return ErrorClassUnknown
	}
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return classified.Class
	}
	// Errors from other packages can classify themselves without this package
	// importing them (the Cairn API client returns typed Worker codes).
	var self interface{ Class() ErrorClass }
	if errors.As(err, &self) {
		return self.Class()
	}
	return ErrorClassUnknown
}

// PausesComponent reports whether the error means the current component cannot
// make progress and should stop claiming new jobs until it is reconfigured or
// the dependency recovers.
func PausesComponent(err error) bool {
	class := ClassOf(err)
	return class == ErrorClassConfiguration || class == ErrorClassContract || class == ErrorClassBudget
}

// IsRetryable reports whether a bounded retry is appropriate.
func IsRetryable(err error) bool { return ClassOf(err) == ErrorClassTransient }

// IsStale reports whether the failure is an expired lease, superseded input or
// changed target rather than a model or source failure.
func IsStale(err error) bool { return ClassOf(err) == ErrorClassStale }

// ClassifyModelError converts a provider HTTP failure into a class. The mapping
// is explicit per status instead of a single "5xx means retry" rule because a
// 409 has several meanings and a 400/401/422 must not consume every attempt.
func ClassifyModelError(err error) error {
	if err == nil {
		return nil
	}
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return err
	}
	var modelErr *ModelHTTPError
	if errors.As(err, &modelErr) {
		switch modelErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
			return classify(ErrorClassConfiguration, err)
		case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusUnsupportedMediaType:
			return classify(ErrorClassContract, err)
		case http.StatusConflict:
			return classify(ErrorClassStale, err)
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return classify(ErrorClassTransient, err)
		default:
			return classify(ErrorClassUnknown, err)
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, net.ErrClosed) || isTransportMessage(err) {
		return classify(ErrorClassTransient, err)
	}
	return classify(ErrorClassUnknown, err)
}

// isTransportMessage recognises the transport failures the standard library
// reports as plain errors rather than typed ones.
func isTransportMessage(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused", "connection reset", "broken pipe",
		"no such host", "tls handshake", "eof", "server closed",
		"context deadline exceeded",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
