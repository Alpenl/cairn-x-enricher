package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// Only fixed, low-risk diagnostic dimensions pass into the optional exporter.
// Call sites may carry private URLs, source text or provider error messages;
// keeping an explicit allowlist makes a new attribute safe by default.
var numericLogFields = map[string]bool{
	"link_id": true, "attempt": true, "claimed": true, "completed": true,
	"failed": true, "classified": true, "pending": true, "provider_calls": true,
	"duration_ms": true, "content_revision": true, "original_text_bytes": true,
	"related_links": true, "images": true, "discarded_tags": true,
	"declared": true, "written": true, "timeout": true,
}

func safeLogAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if safe, ok := safeLogAttr(attr); ok {
			out = append(out, safe)
		}
	}
	return out
}

func safeLogAttr(attr slog.Attr) (slog.Attr, bool) {
	if attr.Key == "error" {
		return slog.String("error_code", safeErrorCode(attr.Value.Any())), true
	}
	if numericLogFields[attr.Key] {
		switch attr.Value.Kind() {
		case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindDuration:
			return attr, true
		}
		return slog.Attr{}, false
	}
	if attr.Value.Kind() == slog.KindString && safeLogDimension(attr.Key, attr.Value.String()) {
		return attr, true
	}
	return slog.Attr{}, false
}

func safeLogDimension(key, value string) bool {
	switch key {
	case "stage":
		return oneOf(value, "source", "fetch", "reading", "classification", "evidence")
	case "component":
		return oneOf(value, "source", "classification", "evidence", "web", "scheduler")
	case "status":
		return oneOf(value, "pending", "processing", "completed", "failed", "rejected", "fetching", "applying")
	case "reason":
		return oneOf(value, "lease_released", "lease_conflict", "provider_result_unknown")
	case "error_class":
		return value == safeErrorClass(enrich.ErrorClass(value))
	case "request_id":
		return safeUUID(value)
	default:
		return false
	}

}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func safeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !hexDigit(char) {
			return false
		}
	}
	return true
}

func hexDigit(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}

func safeErrorCode(value any) string {
	err, ok := value.(error)
	if !ok || err == nil {
		return "other_error"
	}
	var apiErr *cairn.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("worker_http_%d_%s", apiErr.StatusCode, safeErrorClass(apiErr.Class()))
	}
	var modelErr *enrich.ModelHTTPError
	if errors.As(err, &modelErr) {
		return fmt.Sprintf("provider_http_%d", modelErr.StatusCode)
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline"
	}
	return safeErrorClass(enrich.ClassOf(err))
}

func safeErrorClass(class enrich.ErrorClass) string {
	switch class {
	case enrich.ErrorClassConfiguration, enrich.ErrorClassContract,
		enrich.ErrorClassTransient, enrich.ErrorClassStale,
		enrich.ErrorClassCompleted, enrich.ErrorClassBudget:
		return string(class)
	default:
		return string(enrich.ErrorClassUnknown)
	}
}
