package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// PolicyReplayRequest is a controlled zero-inference decision over unchanged
// stored input. The Worker validates source/spec/model and both CAS revisions.
type PolicyReplayRequest struct {
	OperationKey             string                 `json:"operation_key"`
	RunIDs                   []int64                `json:"run_ids"`
	PolicyVersion            string                 `json:"policy_version"`
	Policy                   classify.Policy        `json:"policy"`
	PolicyHash               string                 `json:"policy_hash"`
	Automatic                classify.AutomaticView `json:"automatic"`
	ExpectedRevision         int64                  `json:"expected_revision"`
	ContentRevision          int64                  `json:"content_revision"`
	SpecID                   string                 `json:"spec_id"`
	SpecHash                 string                 `json:"spec_hash"`
	RequestedModel           string                 `json:"requested_model"`
	ResolvedModel            string                 `json:"resolved_model"`
	ExpectedTargetGeneration int64                  `json:"expected_target_generation"`
}

// SubmitPolicyReplay appends a source-bound decision without invoking a provider.
func (c *Client) SubmitPolicyReplay(ctx context.Context, id int64, body PolicyReplayRequest) (json.RawMessage, error) {
	if id < 1 || body.OperationKey == "" || body.PolicyHash == "" || len(body.RunIDs) == 0 {
		return nil, errors.New("invalid controlled policy replay")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v2/links/%d/policy-replays", id), body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var receipt json.RawMessage
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return nil, err
	}
	var identity struct {
		PolicyHash       string `json:"policy_hash"`
		PolicyVersion    string `json:"policy_version"`
		TargetGeneration int64  `json:"target_generation"`
	}
	if err := json.Unmarshal(receipt, &identity); err != nil {
		return nil, err
	}
	if identity.PolicyHash != body.PolicyHash || identity.PolicyVersion != body.PolicyVersion || identity.TargetGeneration != body.ExpectedTargetGeneration {
		return nil, errors.New("controlled policy replay receipt identity mismatch")
	}
	return receipt, nil
}
