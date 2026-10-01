package cairn

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// SubmitClassificationAttempts appends audit receipts without changing queue,
// suggestions or human edits. Repeating exactly the same body is idempotent.
func (c *Client) SubmitClassificationAttempts(ctx context.Context, job *ClassificationJob, calls []classify.ProviderCall) error {
	if job == nil || len(calls) == 0 {
		return errors.New("classification attempt receipt requires a job and calls")
	}
	body := map[string]any{
		"link_id": job.ID, "lease_token": job.LeaseToken, "revision": job.Revision,
		"input_revision": job.InputRevision, "target_generation": job.TargetGeneration, "spec_id": job.SpecID,
		"content_revision": job.ContentRevision, "evidence_snapshot_id": job.EvidenceSnapshotID, "evidence_hash": job.EvidenceHash, "calls": calls,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	body["operation_key"] = fmt.Sprintf("attempts-%x", sha256.Sum256(encoded))
	response, err := c.doWithHeaders(ctx, http.MethodPost, "/api/v2/classification-attempts", body, map[string]string{"X-Cairn-Classification-Attempts": "1"})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	if response.Header.Get("X-Cairn-Classification-Attempts") != "1" {
		return errors.New("classification attempt receipt capability mismatch")
	}
	var receipt struct {
		Stored       bool    `json:"stored"`
		Replayed     bool    `json:"replayed"`
		OperationKey string  `json:"operation_key"`
		AttemptIDs   []int64 `json:"attempt_ids"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return err
	}
	if !receipt.Stored || receipt.OperationKey != body["operation_key"] || len(receipt.AttemptIDs) != len(calls) {
		return errors.New("invalid classification attempt receipt")
	}
	return nil
}
