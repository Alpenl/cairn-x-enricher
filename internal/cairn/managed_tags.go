package cairn

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// AdoptManagedCatalog is a management operation limited to a human-authored
// catalog. The Worker validates its current version and fences the generation.
func (c *Client) AdoptManagedCatalog(ctx context.Context, current ClassificationTarget, spec classify.QuestionSpec, model string) error {
	hash, err := classify.HashSpec(spec)
	if err != nil {
		return err
	}
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/classifications/target", map[string]any{
		"expected_generation": current.Generation, "spec_id": spec.SpecID, "spec_hash": hash, "taxonomy_version": spec.TaxonomyVersion,
		"policy_version": current.PolicyVersion, "requested_model": model, "protocol": "v2", "new_items_only": true, "note": "human managed tag catalog; pending and new items only"})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var result json.RawMessage
	return decodeJSON(response.Body, &result)
}

// DrainCollectionRules processes bounded, deterministic collection membership work.
func (c *Client) DrainCollectionRules(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/collection-rules/drain", map[string]any{})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	return nil
}
