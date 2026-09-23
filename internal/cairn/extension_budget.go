package cairn

import (
	"context"
	"errors"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

// ReserveExtensionBudget never retries a grant or treats legacy 404 as success.
// If the response is lost, the server keeps the charge and the caller performs
// no external operation. Restarting the client cannot reset the D1 counters.
func (c *Client) ReserveExtensionBudget(ctx context.Context, reservation extension.Reservation) (extension.Grant, error) {
	response, err := c.do(ctx, http.MethodPost, "/api/v2/extension-budget/reserve", reservation)
	if err != nil {
		return extension.Grant{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return extension.Grant{}, apiError(response)
	}
	var grant extension.Grant
	if err := decodeJSON(response.Body, &grant); err != nil {
		return extension.Grant{}, err
	}
	if (grant.Granted && grant.Reason != "reserved") || (!grant.Granted && grant.Reason != "already_reserved" && grant.Reason != "budget_exhausted") {
		return extension.Grant{}, errors.New("invalid extension budget acknowledgment")
	}
	return grant, nil
}
