package cairn

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

var entityKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ClaimEntity obtains a single durable owner or an existing result.
func (c *Client) ClaimEntity(ctx context.Context, claim extension.EntityClaim) (extension.EntityReceipt, error) {
	return c.entityRequest(ctx, http.MethodPost, "/api/v2/entity-cache/claim", claim)
}

// CompleteEntity persists exactly the original owner's bounded result.
func (c *Client) CompleteEntity(ctx context.Context, key string, completion extension.EntityCompletion) (extension.EntityReceipt, error) {
	if !entityKeyPattern.MatchString(key) {
		return extension.EntityReceipt{}, errors.New("invalid entity cache key")
	}
	return c.entityRequest(ctx, http.MethodPost, "/api/v2/entity-cache/"+key+"/complete", completion)
}

// GetEntity recovers a committed result without repeating inference.
func (c *Client) GetEntity(ctx context.Context, key string) (extension.EntityReceipt, error) {
	if !entityKeyPattern.MatchString(key) {
		return extension.EntityReceipt{}, errors.New("invalid entity cache key")
	}
	return c.entityRequest(ctx, http.MethodGet, "/api/v2/entity-cache/"+key, nil)
}

func (c *Client) entityRequest(ctx context.Context, method, path string, body any) (extension.EntityReceipt, error) {
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return extension.EntityReceipt{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return extension.EntityReceipt{}, apiError(response)
	}
	var receipt extension.EntityReceipt
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return receipt, err
	}
	if !entityKeyPattern.MatchString(receipt.Key) || receipt.ExpiresAt < 1 || receipt.Answers == nil || (receipt.Status != "pending" && receipt.Status != "completed" && receipt.Status != "failed") || (receipt.Owned && receipt.Status != "pending") {
		return extension.EntityReceipt{}, errors.New("invalid entity cache receipt")
	}
	return receipt, nil
}
