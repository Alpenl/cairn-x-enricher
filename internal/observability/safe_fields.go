package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// SafeJSONHandler applies the same privacy boundary to commands that run
// without the optional asynchronous observability exporter.
func SafeJSONHandler(writer io.Writer, level slog.Level) slog.Handler {
	return &safeLogHandler{next: slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level})}
}

type safeLogHandler struct {
	next slog.Handler
}

func (h *safeLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *safeLogHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, safeLogRecord(record))
}

func (h *safeLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &safeLogHandler{next: h.next.WithAttrs(safeLogAttrs(attrs))}
}

func (h *safeLogHandler) WithGroup(name string) slog.Handler {
	return &safeLogHandler{next: h.next.WithGroup(safeLogGroup(name))}
}

func safeLogRecord(record slog.Record) slog.Record {
	clean := slog.NewRecord(record.Time, record.Level, safeLogMessage(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if safe, ok := safeLogAttr(attr); ok {
			clean.AddAttrs(safe)
		}
		return true
	})
	return clean
}

func safeLogGroup(name string) string {
	if name == "task" {
		return name
	}
	return "group"
}

// Only fixed, low-risk diagnostic dimensions pass into the optional exporter.
// Call sites may carry private URLs, source text or provider error messages;
// keeping an explicit allowlist makes a new attribute safe by default.
var numericLogFields = map[string]bool{
	"attempt": true, "claimed": true, "completed": true,
	"failed": true, "classified": true, "pending": true, "provider_calls": true,
	"duration_ms": true, "content_revision": true, "original_text_bytes": true,
	"related_links": true, "images": true, "discarded_tags": true,
	"declared": true, "written": true, "timeout": true,
	"schema_version": true, "provider_http_status": true,
	"input_tokens": true, "output_tokens": true, "total_tokens": true,
	"x_search_calls": true, "cost_usd_ticks": true,
	"backoff_ms": true,
}

// stdout may be copied to a backend that cannot delete one bookmark's
// records. Only reviewed, fixed messages may leave the optional exporter.
// Unknown messages can contain URLs, source text or provider error bodies.
func safeLogMessage(message string) string {
	switch message {
	case "provider attempt", "health server listening", "shutdown requested", "scheduled batch worker panicked",
		"scheduled batch failed", "scheduled batch finished",
		"classification round stopped", "classification round finished",
		"backend has no v2 taxonomy; running the legacy single-dimension vocabulary",
		"backend has no v2 question-spec endpoint; stored runs will not be replayable",
		"enrichment started", "enrichment completed", "enrichment interrupted", "enrichment failed",
		"failed to inspect existing enrichment detail", "recovering partial enrichment from existing source text",
		"failed to store enrichment", "failed to report enrichment failure", "classification requires review",
		"evidence recovery listing failed", "evidence execution claim failed", "evidence checkpoint failed",
		"evidence result remains recoverable", "paid stage deferred after source lease check",
		"could not defer unused paid stage after budget denial", "paid stage deferred until next budget window",
		"source refreshed; classification queued", "source saved; classification queued",
		"evidence snapshots unsupported by backend; continuing in v1 mode",
		"classification inference attempt failed; no inference fallback",
		"classification failed; source retained", "extensions require verified bound evidence",
		"entity state was not stored", "evidence request was not stored", "evidence request queued",
		"draining manual jobs", "shutdown drain timed out; unfinished jobs keep their lease and will be retried",
		"get image returned unsafe content type", "truncated image response", "stream image response",
		"manual request rejected", "manual source save rejected", "manual source persisted",
		"manual enrichment failed", "manual enrichment completed", "source stage event":
		return message
	default:
		return "application_event"
	}
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
		return oneOf(value, "source", "fetch", "reading", "canary", "classification", "evidence")
	case "event_name":
		return oneOf(value, "provider_attempt_reserved", "provider_attempt_denied",
			"provider_attempt_dispatching", "provider_response_headers_received",
			"provider_attempt_responded", "provider_attempt_unknown",
			"local_stage_paused", "stage_probe_started", "stage_probe_succeeded",
			"stage_probe_failed", "stage_probe_released", "stage_probe_superseded",
			"claim_skipped_local_pause",
			"local_defer_succeeded", "local_defer_failed")
	case "provider_variant":
		return oneOf(value, "fetch_thread", "fetch_post", "reading", "canary")
	case "provider_reason":
		return oneOf(value, "already_reserved", "network_unknown", "decode_failed", "settlement_failed")
	case "component":
		return oneOf(value, "source", "classification", "evidence", "web", "scheduler")
	case "status":
		return oneOf(value, "pending", "processing", "completed", "failed", "rejected", "fetching", "applying")
	case "reason":
		return oneOf(value, "lease_released", "lease_conflict", "provider_result_unknown")
	case "error_class":
		return value == safeErrorClass(enrich.ErrorClass(value))
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

// SafeErrorCode is suitable for the top-level command failure line, which may
// be captured by Docker even when the structured exporter is disabled.
func SafeErrorCode(err error) string { return safeErrorCode(err) }

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
