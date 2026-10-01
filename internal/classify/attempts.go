package classify

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// ProviderAttemptErrorClass is deliberately finite and contains no provider
// body, URL, credential or private error string.
func ProviderAttemptErrorClass(err error, status int) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if status == 401 || status == 403 {
		return "auth"
	}
	if status == 429 {
		return "rate_limit"
	}
	if status >= 500 {
		return "provider_5xx"
	}
	if status >= 400 {
		return "provider_4xx"
	}
	if enrich.ClassOf(err) == enrich.ErrorClassContract {
		return "contract_fault"
	}
	if enrich.ClassOf(err) == enrich.ErrorClassTransient {
		return "network"
	}
	return "unknown"
}

// AttemptReceiptCalls exports only real reserved calls and normalized usage.
// An unavailable usage count stays unknown instead of becoming a zero charge.
func AttemptReceiptCalls(calls []ProviderCall, inferenceErr error) []ProviderCall {
	out := make([]ProviderCall, 0, len(calls))
	for _, call := range calls {
		if call.ReservationKey == "" {
			continue
		} // offline or pre-admission denial
		input, output, ok := tokenUsage(call.Usage)
		call.UsageMissing = !ok
		call.Usage = nil
		if ok {
			call.Usage, _ = json.Marshal(map[string]int64{"input_tokens": input, "output_tokens": output})
		}
		if inferenceErr != nil && (call.ErrorClass == "" || call.ErrorClass == "none") {
			call.ErrorClass = ProviderAttemptErrorClass(inferenceErr, call.HTTPStatus)
		}
		if call.ErrorClass == "" {
			call.ErrorClass = "none"
		}
		out = append(out, call)
	}
	return out
}
