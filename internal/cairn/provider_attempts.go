package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

const providerAttemptPath = "/api/enrichment/provider-attempts"

// DeferSourceBudget returns an unused leased stage to the queue at the next
// UTC budget window, without charging a task attempt or starting a model POST.
func (c *Client) DeferSourceBudget(ctx context.Context, id int64, leaseToken, stage string) error {
	if id < 1 || leaseToken == "" || (stage != "fetch" && stage != "reading") {
		return errors.New("invalid source budget deferral")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/enrichment/jobs/%d/budget-defer", id),
		map[string]string{"lease_token": leaseToken, "stage": stage})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var receipt struct {
		ID      int64  `json:"id"`
		Status  string `json:"status"`
		RetryAt string `json:"retry_at"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return fmt.Errorf("decode source budget deferral: %w", err)
	}
	if receipt.ID != id || receipt.Status != "deferred" || receipt.RetryAt == "" {
		return errors.New("source budget deferral receipt is invalid")
	}
	return nil
}

// ReserveProviderAttempt returns true only for the one call allowed to send a
// provider POST. Replaying a lost Worker response can never grant it again.
func (c *Client) ReserveProviderAttempt(ctx context.Context, attempt enrich.ProviderAttempt) (bool, error) {
	response, err := c.do(ctx, http.MethodPost, providerAttemptPath+"/reserve", attempt)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false, apiError(response)
	}
	var receipt struct {
		Granted bool   `json:"granted"`
		Reason  string `json:"reason"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return false, fmt.Errorf("decode provider attempt reservation: %w", err)
	}
	if receipt.Granted && receipt.Reason != "reserved" ||
		!receipt.Granted && receipt.Reason != "already_reserved" {
		return false, errors.New("invalid provider attempt reservation receipt")
	}
	return receipt.Granted, nil
}

// SettleProviderAttempt records the known result of one reserved provider POST.
func (c *Client) SettleProviderAttempt(ctx context.Context, settlement enrich.ProviderSettlement) error {
	response, err := c.do(ctx, http.MethodPost, providerAttemptPath+"/settle", settlement)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var receipt struct {
		Settled bool `json:"settled"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return fmt.Errorf("decode provider attempt settlement: %w", err)
	}
	if !receipt.Settled {
		return errors.New("provider attempt settlement receipt is invalid")
	}
	return nil
}

// AuthorizeProviderFallback permits the one alternate source prompt after a known first response.
func (c *Client) AuthorizeProviderFallback(ctx context.Context, operationKey string) error {
	response, err := c.do(ctx, http.MethodPost, providerAttemptPath+"/authorize-fallback",
		map[string]string{"operation_key": operationKey})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var receipt struct {
		Authorized bool `json:"authorized"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return fmt.Errorf("decode source fallback authorization: %w", err)
	}
	if !receipt.Authorized {
		return errors.New("source fallback authorization receipt is invalid")
	}
	return nil
}
