package cairn

import (
	"context"
	"net/http"
)

// ProbeCandidateManifestCapability prevents selected-only evaluations from
// reaching a Worker that still requires every full-spec answer. The target
// request is read-only and has no provider or queue side effects.
func (c *Client) ProbeCandidateManifestCapability(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/classifications/target", nil)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	if response.Header.Get("X-Cairn-Candidate-Manifest") != "2" {
		return &APIError{StatusCode: http.StatusConflict, Code: "unsupported_candidate_manifest_contract"}
	}
	return nil
}
