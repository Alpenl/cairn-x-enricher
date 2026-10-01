package classify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestAttemptReceiptPreservesUnknownUsageAndDropsUnreservedCalls(t *testing.T) {
	raw := []ProviderCall{{RequestHash: "pre-admission"}, {ReservationKey: "real", RequestHash: "actual", HTTPStatus: 200, Usage: json.RawMessage(`{"input_tokens":12,"output_tokens":0,"private_extension":"omit"}`)}, {ReservationKey: "unknown", RequestHash: "actual-unknown", UsageMissing: true}}
	calls := AttemptReceiptCalls(raw, enrich.Classified(errors.New("private invalid schema"), enrich.ErrorClassContract))
	if len(calls) != 2 || calls[0].ErrorClass != "contract_fault" || calls[0].UsageMissing || string(calls[0].Usage) != `{"input_tokens":12,"output_tokens":0}` || !calls[1].UsageMissing || calls[1].Usage != nil {
		t.Fatalf("bad audit normalization: %+v", calls)
	}
	if len(raw[1].Usage) == 0 || raw[1].ErrorClass != "" {
		t.Fatal("receipt mutated original raw call")
	}
	if ProviderAttemptErrorClass(context.DeadlineExceeded, 0) != "timeout" || ProviderAttemptErrorClass(errors.New("HTTP fault"), 429) != "rate_limit" {
		t.Fatal("receipt error taxonomy changed")
	}
}
