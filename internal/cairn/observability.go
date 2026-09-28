package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ObservabilityConfig is the small, non-private control-plane payload sent to
// Share. Metric and trace switches will be separate fields when exporters exist.
type ObservabilityConfig struct {
	Version         int64  `json:"version"`
	Logs            string `json:"logs"`
	FallbackLogs    string `json:"fallback_logs,omitempty"`
	DiagnosticUntil int64  `json:"diagnostic_until,omitempty"`
}

// ObservabilityReceipt confirms the Worker's durably stored policy version.
type ObservabilityReceipt struct {
	Version       int64  `json:"version"`
	EffectiveLogs string `json:"effective_logs"`
}

// PublishObservability is safe to retry after a lost response: Share accepts
// the same version and payload, and rejects changed or older versions.
func (c *Client) PublishObservability(ctx context.Context, config ObservabilityConfig) (ObservabilityReceipt, error) {
	response, err := c.do(ctx, http.MethodPost, "/api/internal/observability", config)
	if err != nil {
		return ObservabilityReceipt{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return ObservabilityReceipt{}, apiError(response)
	}
	var receipt ObservabilityReceipt
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return ObservabilityReceipt{}, fmt.Errorf("decode observability receipt: %w", err)
	}
	if receipt.Version != config.Version ||
		(receipt.EffectiveLogs != "off" && receipt.EffectiveLogs != "basic" && receipt.EffectiveLogs != "diagnostic") {
		return ObservabilityReceipt{}, errors.New("invalid observability receipt")
	}
	return receipt, nil
}
