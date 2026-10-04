package cairn

import (
	"context"
	"fmt"
	"net/http"
)

// ProviderCheckStatus contains only safe, configuration-scoped check metadata.
type ProviderCheckStatus struct {
	State         string `json:"state"`
	NextCheckAt   int64  `json:"next_check_at"`
	LastSuccessAt int64  `json:"last_success_at"`
	ValidUntil    int64  `json:"valid_until"`
	Reason        string `json:"reason"`
	Failures      int    `json:"failures"`
	ManualAfter   int64  `json:"manual_after"`
	CanRecover    bool   `json:"can_recover"`
	Granted       bool   `json:"granted,omitempty"`
	Accepted      bool   `json:"accepted,omitempty"`
	LeaseToken    string `json:"lease_token,omitempty"`
}

// ProviderCheck coordinates a check without exposing credentials or article content.
func (c *Client) ProviderCheck(ctx context.Context, scope, action string, result map[string]any) (ProviderCheckStatus, error) {
	path := "/api/enrichment/provider-checks/" + action
	method := http.MethodPost
	body := map[string]any{"scope": scope}
	for k, v := range result {
		body[k] = v
	}
	if action == "status" {
		method = http.MethodGet
		path += "?scope=" + scope
		body = nil
	}
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return ProviderCheckStatus{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return ProviderCheckStatus{}, apiError(response)
	}
	var status ProviderCheckStatus
	if err := decodeJSON(response.Body, &status); err != nil {
		return status, fmt.Errorf("decode provider check: %w", err)
	}
	if status.State != "healthy" && status.State != "pending" && status.State != "waiting" && status.State != "checking" {
		return status, fmt.Errorf("invalid provider check state")
	}
	if status.Granted && status.LeaseToken == "" {
		return status, fmt.Errorf("missing provider check lease")
	}
	return status, nil
}
