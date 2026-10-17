package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// PolicyPreviewCapability identifies the internally prepared, zero-model App contract.
const PolicyPreviewCapability = "X-Cairn-App-Policy-Replay"

// ErrInvalidPolicyPreview is a local refusal before a network request is sent.
var ErrInvalidPolicyPreview = errors.New("invalid policy preview intent")

var policyPreviewKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// GetPolicyPreview confirms a persisted preview or its applied decision. An
// omitted hash is only useful while recovering a lost preparation response.
func (c *Client) GetPolicyPreview(ctx context.Context, id int64, key, hash string) (json.RawMessage, error) {
	if !policyPreviewKey.MatchString(key) || hash != "" && !materialHash(hash) {
		return nil, ErrInvalidPolicyPreview
	}
	query := url.Values{"preview_key": {key}}
	if hash != "" {
		query.Set("preview_hash", hash)
	}
	return c.policyPreview(ctx, id, "previews?"+query.Encode(), nil, key, hash, nil)
}

// PreparePolicyPreview persists a locally computed plan without applying it.
// The Worker assigns its commit key and validates the complete decision guard.
func (c *Client) PreparePolicyPreview(ctx context.Context, id int64, key string, plan PolicyReplayRequest) (json.RawMessage, error) {
	if !policyPreviewKey.MatchString(key) || plan.OperationKey != "" || !validPreviewPlan(plan) {
		return nil, ErrInvalidPolicyPreview
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return nil, ErrInvalidPolicyPreview
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil {
		return nil, ErrInvalidPolicyPreview
	}
	delete(fields, "operation_key")
	return c.policyPreview(ctx, id, "previews", map[string]any{"version": 1, "preview_key": key, "plan": fields}, key, "", &plan)
}

// ApplyPolicyPreview sends only the approved identity. No caller-supplied
// policy, automatic labels, spec or model can enter the stored plan.
func (c *Client) ApplyPolicyPreview(ctx context.Context, id int64, key, hash string) (json.RawMessage, error) {
	if !policyPreviewKey.MatchString(key) || !materialHash(hash) {
		return nil, ErrInvalidPolicyPreview
	}
	return c.policyPreview(ctx, id, "replays", map[string]any{"version": 1, "preview_key": key, "preview_hash": hash}, key, hash, nil)
}

func (c *Client) policyPreview(ctx context.Context, id int64, suffix string, value any, key, hash string, expected *PolicyReplayRequest) (json.RawMessage, error) {
	if !materialPositive(id) {
		return nil, ErrInvalidPolicyPreview
	}
	method := http.MethodGet
	var body io.Reader
	if value != nil {
		payload, err := json.Marshal(value)
		if err != nil || len(payload) > 128<<10 {
			return nil, ErrInvalidPolicyPreview
		}
		method, body = http.MethodPost, bytes.NewReader(payload)
	}
	transport := *c.httpClient
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client := NewClient(c.baseURL, c.token, &transport)
	response, err := client.doRequest(ctx, method, fmt.Sprintf("/api/v2/links/%d/app-policy-%s", id, suffix), body,
		map[string]string{PolicyPreviewCapability: "1"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	marked := response.Header.Get(PolicyPreviewCapability) == "1"
	if response.StatusCode == 405 || response.StatusCode == 501 || response.StatusCode == 404 && !marked {
		return nil, ErrV2Unsupported
	}
	if !marked {
		return nil, errors.New("policy preview capability acknowledgement missing")
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > 256<<10 || !utf8.Valid(payload) || !validPolicyPreview(payload, id, key, hash, expected, suffix == "replays") {
		return nil, errors.New("invalid policy preview response")
	}
	return json.RawMessage(payload), nil
}

func validPreviewPlan(plan PolicyReplayRequest) bool {
	hash, err := classify.PolicyHash(plan.Policy)
	return err == nil && plan.Policy.Validate() == nil && hash == plan.PolicyHash && plan.PolicyVersion == plan.Policy.Version &&
		len(plan.RunIDs) == 1 && materialPositive(plan.RunIDs[0]) && materialPositive(plan.ContentRevision) &&
		plan.ExpectedRevision >= 0 && plan.ExpectedRevision <= 1<<53-1 && plan.ExpectedTargetGeneration >= 0 && plan.ExpectedTargetGeneration <= 1<<53-1 &&
		materialText(plan.SpecID, 64) && materialHash(plan.SpecHash) && materialText(plan.RequestedModel, 200) && materialText(plan.ResolvedModel, 200)
}

func validPolicyPreview(payload []byte, id int64, key, hash string, expected *PolicyReplayRequest, requireApplied bool) bool {
	var value struct {
		Version    int             `json:"version"`
		ID         int64           `json:"id"`
		Key        string          `json:"preview_key"`
		Hash       string          `json:"preview_hash"`
		Status     string          `json:"status"`
		Calls      *int            `json:"model_calls"`
		Content    *int64          `json:"content_revision"`
		Personal   *int64          `json:"personal_revision"`
		Target     *int64          `json:"target_generation"`
		Policy     string          `json:"policy_version"`
		PolicyHash string          `json:"policy_hash"`
		ReviewHash string          `json:"review_hash"`
		Plan       json.RawMessage `json:"plan"`
		Before     json.RawMessage `json:"before"`
		After      json.RawMessage `json:"after"`
		DecisionID int64           `json:"decision_id"`
		Revision   *int64          `json:"revision"`
		Replayed   *bool           `json:"replayed"`
	}
	if json.Unmarshal(payload, &value) != nil || value.Version != 1 || value.ID != id || value.Key != key || !materialHash(value.Hash) ||
		hash != "" && value.Hash != hash || value.Calls == nil || *value.Calls != 0 || value.Target == nil || *value.Target < 0 || *value.Target > 1<<53-1 ||
		!materialText(value.Policy, 100) || !materialHash(value.PolicyHash) {
		return false
	}
	if requireApplied && value.Status != "applied" || expected != nil && (value.Policy != expected.PolicyVersion ||
		value.PolicyHash != expected.PolicyHash || *value.Target != expected.ExpectedTargetGeneration) {
		return false
	}
	if value.Status == "applied" {
		return materialPositive(value.DecisionID) && value.Revision != nil && *value.Revision >= 0 && *value.Revision <= 1<<53-1 &&
			value.Replayed != nil && (expected == nil || *value.Revision == expected.ExpectedRevision)
	}
	if value.Status != "prepared" || value.Content == nil || !materialPositive(*value.Content) || value.Personal == nil || *value.Personal < 0 ||
		*value.Personal > 1<<53-1 || !materialHash(value.ReviewHash) || !validPreviewView(value.Before) || !validPreviewView(value.After) {
		return false
	}
	var plan PolicyReplayRequest
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(value.Plan))
	decoder.DisallowUnknownFields()
	if json.Unmarshal(value.Plan, &fields) != nil || len(fields) != 13 || decoder.Decode(&plan) != nil || !validPreviewPlan(plan) {
		return false
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return false
		}
	}
	if plan.OperationKey != fmt.Sprintf("app-policy:%d:%s:%s", id, key, value.Hash) || plan.ContentRevision != *value.Content ||
		plan.ExpectedRevision != *value.Personal || plan.ExpectedTargetGeneration != *value.Target || plan.PolicyVersion != value.Policy || plan.PolicyHash != value.PolicyHash {
		return false
	}
	if expected != nil {
		plan.OperationKey = ""
		a, _ := json.Marshal(plan)
		b, _ := json.Marshal(expected)
		return bytes.Equal(a, b)
	}
	return true
}

func validPreviewView(payload []byte) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(payload, &value) != nil || value == nil {
		return false
	}
	for _, field := range []string{"topics", "content_functions", "carriers", "affordances", "entities", "resource_kinds"} {
		if field == "resource_kinds" && value[field] == nil {
			continue
		}
		var items []string
		if json.Unmarshal(value[field], &items) != nil || items == nil || len(items) > 1000 {
			return false
		}
	}
	for _, field := range []string{"form", "use"} {
		var text string
		if json.Unmarshal(value[field], &text) != nil {
			return false
		}
	}
	return true
}
