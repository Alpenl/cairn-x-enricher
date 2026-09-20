package cairn

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// ClassificationJob identifies a leased, versioned snapshot for semantic work.
type ClassificationJob struct {
	ID         int64  `json:"id"`
	Revision   int64  `json:"revision"`
	Attempt    int    `json:"attempt"`
	LeaseToken string `json:"lease_token"`
	LeaseUntil string `json:"lease_until"`
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
func (c *Client) ClaimClassification(ctx context.Context, version, model string) (*ClassificationJob, error) {
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/classifications/claim", map[string]string{
		"taxonomy_version": version, "policy_version": classify.PolicyVersion, "model": model})
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

// CompleteClassification commits suggestions only for the current input revision.
func (c *Client) CompleteClassification(ctx context.Context, job *ClassificationJob, result classify.Result) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/complete", job.ID), map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "result": result})
}

// FailClassification schedules durable backoff without changing saved content.
func (c *Client) FailClassification(ctx context.Context, job *ClassificationJob, message string) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/fail", job.ID), map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "error": message})
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
