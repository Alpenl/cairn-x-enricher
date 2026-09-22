package cairn

import (
	"context"
	"errors"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// ReserveClassificationBudget performs exactly one admission request. A lost
// acknowledgment consumes the reservation but never invokes the provider.
func (c *Client) ReserveClassificationBudget(ctx context.Context, reservation classify.CallReservation) (classify.CallGrant, error) {
	response, err := c.do(ctx, http.MethodPost, "/api/v2/classification-budget/reserve", reservation)
	if err != nil {
		return classify.CallGrant{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return classify.CallGrant{}, apiError(response)
	}
	var grant classify.CallGrant
	if err := decodeJSON(response.Body, &grant); err != nil {
		return grant, err
	}
	if (grant.Granted && grant.Reason != "reserved") || (!grant.Granted && grant.Reason != "budget_exhausted" && grant.Reason != "already_reserved") {
		return classify.CallGrant{}, errors.New("invalid classification budget acknowledgment")
	}
	return grant, nil
}

// SetClassificationBudgetLimits also prevents exhausted daily admission from
// consuming another queue attempt on a subsequent poll or process restart.
func (c *Client) SetClassificationBudgetLimits(limits classify.CallBudgetLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	c.classificationBudget = &limits
	return nil
}
