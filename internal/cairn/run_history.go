package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// RunSummary contains no source, answers, usage or raw request metadata. Newest
// summaries are read first; only a chosen complete run needs the large payload.
type RunSummary struct {
	ID                 int64  `json:"id"`
	ContentRevision    int64  `json:"content_revision"`
	SpecID             string `json:"spec_id"`
	SpecHash           string `json:"spec_hash"`
	TargetGeneration   int64  `json:"target_generation"`
	RequestedModel     string `json:"requested_model"`
	ResolvedModel      string `json:"resolved_model"`
	PolicyVersion      string `json:"policy_version"`
	Attempt            int    `json:"attempt"`
	Coverage           string `json:"coverage"`
	EvidenceCoverage   string `json:"evidence_coverage"`
	AliasDrift         bool   `json:"alias_drift"`
	Status             string `json:"status"`
	CreatedAt          string `json:"created_at"`
	EvidenceSnapshotID int64  `json:"evidence_snapshot_id"`
	SourceHash         string `json:"source_hash"`
	Archived           bool   `json:"archived"`
}

// RunSummaryPage is one bounded page of metadata and its continuation cursor.
type RunSummaryPage struct {
	Runs        []RunSummary `json:"runs"`
	NextAfterID *int64       `json:"next_after_id"`
}

// GetRunSummaryPage reads newest-first metadata without loading answer payloads.
func (c *Client) GetRunSummaryPage(ctx context.Context, id, after int64) (RunSummaryPage, error) {
	if id < 1 || after < 0 {
		return RunSummaryPage{}, errors.New("invalid run history cursor")
	}
	path := fmt.Sprintf("/api/v2/links/%d/runs?view=summary&limit=50", id)
	if after > 0 {
		path += fmt.Sprintf("&after_id=%d", after)
	}
	response, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, map[string]string{"X-Cairn-Run-History": "1"})
	if err != nil {
		return RunSummaryPage{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == 404 || response.StatusCode == 405 {
		return RunSummaryPage{}, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return RunSummaryPage{}, apiError(response)
	}
	if response.Header.Get("X-Cairn-Run-History") != "1" {
		return RunSummaryPage{}, ErrV2Unsupported
	}
	var page RunSummaryPage
	if err := decodeJSON(response.Body, &page); err != nil {
		return page, err
	}
	if len(page.Runs) > 50 {
		return page, errors.New("run summary page exceeds requested bound")
	}
	previous := after
	for _, run := range page.Runs {
		if run.ID < 1 || previous > 0 && run.ID >= previous {
			return page, errors.New("run history cursor did not progress")
		}
		previous = run.ID
	}
	if page.NextAfterID != nil && (len(page.Runs) == 0 || *page.NextAfterID != previous) {
		return page, errors.New("invalid run history next cursor")
	}
	return page, nil
}

// GetRunDetail retrieves one complete run, including a verified archived payload.
func (c *Client) GetRunDetail(ctx context.Context, id, runID int64) (StoredRun, error) {
	response, err := c.doWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/runs/%d", id, runID), nil, map[string]string{"X-Cairn-Run-History": "1"})
	if err != nil {
		return StoredRun{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return StoredRun{}, apiError(response)
	}
	if response.Header.Get("X-Cairn-Run-History") != "1" {
		return StoredRun{}, errors.New("run detail capability mismatch")
	}
	var run StoredRun
	if err := decodeJSON(response.Body, &run); err != nil {
		return run, err
	}
	if run.ID != runID {
		return run, errors.New("run detail identity mismatch")
	}
	return run, nil
}

// GetReplayableRun keeps legacy compatibility explicit, while a modern Worker
// never serializes all stored wire states just to choose the newest complete run.
func (c *Client) GetReplayableRun(ctx context.Context, id int64) (*StoredRun, error) {
	after := int64(0)
	for {
		page, err := c.GetRunSummaryPage(ctx, id, after)
		if IsUnsupported(err) && after == 0 {
			runs, err := c.GetRuns(ctx, id)
			if err != nil {
				return nil, err
			}
			for i := len(runs) - 1; i >= 0; i-- {
				if runs[i].Status == "succeeded" && runs[i].Coverage == "complete" && len(runs[i].Answers) > 0 {
					return &runs[i], nil
				}
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, summary := range page.Runs {
			if summary.Status != "succeeded" || summary.Coverage != "complete" {
				continue
			}
			run, err := c.GetRunDetail(ctx, id, summary.ID)
			if err != nil {
				return nil, err
			}
			if run.Status != "succeeded" || run.Coverage != "complete" {
				return nil, errors.New("run summary and detail disagree")
			}
			if len(run.Answers) > 0 {
				return &run, nil
			}
		}
		if page.NextAfterID == nil {
			return nil, nil
		}
		after = *page.NextAfterID
	}
}
