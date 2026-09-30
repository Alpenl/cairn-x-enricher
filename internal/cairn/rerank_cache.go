package cairn

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

var rerankKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ClaimRerank obtains a single durable owner or an existing result.
func (c *Client) ClaimRerank(ctx context.Context, claim extension.RerankClaim) (extension.RerankReceipt, error) {
	return c.rerankRequest(ctx, http.MethodPost, "/api/v2/rerank-cache/claim", claim)
}

// CompleteRerank persists exactly the original owner's bounded result.
func (c *Client) CompleteRerank(ctx context.Context, key string, completion extension.RerankCompletion) (extension.RerankReceipt, error) {
	if !rerankKeyPattern.MatchString(key) {
		return extension.RerankReceipt{}, errors.New("invalid rerank cache key")
	}
	return c.rerankRequest(ctx, http.MethodPost, "/api/v2/rerank-cache/"+key+"/complete", completion)
}

// GetRerank recovers a committed result without repeating inference.
func (c *Client) GetRerank(ctx context.Context, key string) (extension.RerankReceipt, error) {
	if !rerankKeyPattern.MatchString(key) {
		return extension.RerankReceipt{}, errors.New("invalid rerank cache key")
	}
	return c.rerankRequest(ctx, http.MethodGet, "/api/v2/rerank-cache/"+key, nil)
}

func (c *Client) rerankRequest(ctx context.Context, method, path string, body any) (extension.RerankReceipt, error) {
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return extension.RerankReceipt{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return extension.RerankReceipt{}, apiError(response)
	}
	var receipt extension.RerankReceipt
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return receipt, err
	}
	if !rerankKeyPattern.MatchString(receipt.Key) || receipt.ExpiresAt < 1 || receipt.Answers == nil || (receipt.Status != "pending" && receipt.Status != "completed" && receipt.Status != "failed") || (receipt.Owned && receipt.Status != "pending") {
		return extension.RerankReceipt{}, errors.New("invalid rerank cache receipt")
	}
	return receipt, nil
}
