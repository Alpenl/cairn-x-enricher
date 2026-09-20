package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// ClassificationTarget is the Worker's authoritative classification goal. The
// consumer declares capabilities; it never defines the target itself.
type ClassificationTarget struct {
	Generation      int64  `json:"generation"`
	SpecID          string `json:"spec_id"`
	SpecHash        string `json:"spec_hash"`
	TaxonomyVersion string `json:"taxonomy_version"`
	PolicyVersion   string `json:"policy_version"`
	RequestedModel  string `json:"requested_model"`
	Protocol        string `json:"protocol"`
}

// Capabilities is what a consumer supports. It is sent with the handshake so
// the Worker can reject an incompatible consumer as a component condition
// instead of draining the queue into repeated per-job failures.
type Capabilities struct {
	Protocol         string   `json:"protocol"`
	SpecIDs          []string `json:"spec_ids"`
	TaxonomyVersions []string `json:"taxonomy_versions"`
	PolicyVersions   []string `json:"policy_versions"`
	Models           []string `json:"models"`
}

// HandshakeResult reports the active target and whether this consumer supports it.
type HandshakeResult struct {
	Target    ClassificationTarget `json:"target"`
	Supported bool                 `json:"supported"`
}

// Handshake queries the authoritative target and declares local capabilities.
// It is read-only: it can never change the server target.
func (c *Client) Handshake(ctx context.Context, caps Capabilities) (HandshakeResult, error) {
	values := url.Values{}
	values.Set("protocol", caps.Protocol)
	if len(caps.SpecIDs) > 0 {
		values.Set("spec_ids", strings.Join(caps.SpecIDs, ","))
	}
	if len(caps.TaxonomyVersions) > 0 {
		values.Set("taxonomy_versions", strings.Join(caps.TaxonomyVersions, ","))
	}
	if len(caps.PolicyVersions) > 0 {
		values.Set("policy_versions", strings.Join(caps.PolicyVersions, ","))
	}
	if len(caps.Models) > 0 {
		values.Set("models", strings.Join(caps.Models, ","))
	}
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/classifications/target?"+values.Encode(), nil)
	if err != nil {
		return HandshakeResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return HandshakeResult{}, apiError(response)
	}
	var result HandshakeResult
	if err := decodeJSON(response.Body, &result); err != nil {
		return HandshakeResult{}, fmt.Errorf("decode handshake: %w", err)
	}
	if result.Target.SpecID == "" || result.Target.Generation < 0 {
		return HandshakeResult{}, errors.New("handshake response is missing the target")
	}
	return result, nil
}

// ClassificationJob identifies a leased, versioned snapshot for semantic work.
type ClassificationJob struct {
	ID               int64  `json:"id"`
	Revision         int64  `json:"revision"`
	InputRevision    int64  `json:"input_revision"`
	TargetGeneration int64  `json:"target_generation"`
	SpecID           string `json:"spec_id"`
	Attempt          int    `json:"attempt"`
	LeaseToken       string `json:"lease_token"`
	LeaseUntil       string `json:"lease_until"`
	classify.Input
}

// GetSource returns a snapshot only when it still matches the saved bookmark.
func (c *Client) GetSource(ctx context.Context, id int64) (*enrich.Source, error) {
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/enrichment/jobs/%d/source", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var source enrich.Source
	if err := decodeJSON(response.Body, &source); err != nil {
		return nil, err
	}
	return &source, nil
}

// SaveSource checkpoints retrieval and atomically queues classification.
func (c *Client) SaveSource(ctx context.Context, id int64, token string, source enrich.Source) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/jobs/%d/source", id), map[string]any{"lease_token": token, "source": source})
}

// ClaimClassification acquires semantic work independently of retrieval leases.
// It first negotiates the authoritative target so the lease is bound to the
// server's goal rather than to the local policy version. A capability mismatch
// is returned as a classified configuration error so the caller pauses the
// component instead of burning the queue's attempts.
func (c *Client) ClaimClassification(ctx context.Context, version, model string) (*ClassificationJob, error) {
	handshake, err := c.Handshake(ctx, ClassificationCapabilities(version, model))
	if err != nil {
		return nil, err
	}
	if !handshake.Supported {
		return nil, enrich.Classified(fmt.Errorf("classification target %q generation %d is not supported by this consumer", handshake.Target.SpecID, handshake.Target.Generation), enrich.ErrorClassConfiguration)
	}
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/classifications/claim", map[string]any{
		"protocol":          "v2",
		"spec_ids":          []string{handshake.Target.SpecID},
		"taxonomy_versions": []string{handshake.Target.TaxonomyVersion},
		"policy_versions":   []string{handshake.Target.PolicyVersion},
		"models":            []string{handshake.Target.RequestedModel},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var job ClassificationJob
	if err := decodeJSON(response.Body, &job); err != nil {
		return nil, err
	}
	if job.ID < 1 || job.Revision < 1 || job.LeaseToken == "" || job.OriginalText == "" {
		return nil, fmt.Errorf("invalid classification job")
	}
	return &job, nil
}

// ClassificationCapabilities describes the single target this build supports.
// Keeping it in one place means the handshake, the claim and a retry can never
// disagree about what this consumer can process.
func ClassificationCapabilities(version, model string) Capabilities {
	return Capabilities{
		Protocol:         "v2",
		SpecIDs:          []string{SpecID},
		TaxonomyVersions: []string{version},
		PolicyVersions:   []string{classify.PolicyVersion},
		Models:           []string{model},
	}
}

// SpecID identifies the immutability contract for the classification question
// set. It changes when the meaning of the questions changes, not when a label
// is renamed.
const SpecID = "classify-v1"

// CompleteClassification commits suggestions only for the current input revision.
// The operation key is derived from the source revision so a retried commit after
// a lost response is idempotent at the Worker: it replays the stored result
// instead of paying for a second classification.
func (c *Client) CompleteClassification(ctx context.Context, job *ClassificationJob, result classify.Result) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/complete", job.ID), map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "input_revision": job.InputRevision,
		"target_generation": job.TargetGeneration, "operation_key": ClassificationOperationKey(job),
		"result": result})
}

// ClassificationOperationKey is deterministic for one input revision and target
// generation, which is exactly the scope of one classification result.
func ClassificationOperationKey(job *ClassificationJob) string {
	return fmt.Sprintf("classify-%d-rev-%d-input-%d-gen-%d", job.ID, job.Revision, job.InputRevision, job.TargetGeneration)
}

// FailClassification schedules durable backoff without changing saved content.
// A stale failure (superseded input or target) is reported with its revision so
// the Worker can avoid spending the new target's attempt budget.
func (c *Client) FailClassification(ctx context.Context, job *ClassificationJob, message string) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/fail", job.ID), map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "input_revision": job.InputRevision,
		"target_generation": job.TargetGeneration, "error": message})
}

// RetryClassification enrolls a historical source or retries an inactive job.
func (c *Client) RetryClassification(ctx context.Context, id int64) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/retry", id), map[string]any{})
}

func (c *Client) stageWrite(ctx context.Context, path string, body any) error {
	response, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	return nil
}
