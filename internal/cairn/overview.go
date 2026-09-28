package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrOverviewUnsupported lets an older Worker use the legacy count queries.
var ErrOverviewUnsupported = errors.New("backend does not support the aggregate overview")

// BookmarkOverview is one Worker snapshot of all navigation and processing
// counts. The version makes a partial or changed contract fail closed.
type BookmarkOverview struct {
	Version   int            `json:"version"`
	Views     map[string]int `json:"views"`
	Counts    BookmarkCounts `json:"counts"`
	Attention int            `json:"attention"`
	Queued    int            `json:"queued"`
}

// GetOverview reads one versioned count snapshot from the Worker.
func (c *Client) GetOverview(ctx context.Context) (BookmarkOverview, error) {
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/overview", nil)
	if err != nil {
		return BookmarkOverview{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return BookmarkOverview{}, ErrOverviewUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return BookmarkOverview{}, apiError(response)
	}
	var result BookmarkOverview
	if err := decodeJSON(response.Body, &result); err != nil {
		return BookmarkOverview{}, fmt.Errorf("decode aggregate overview: %w", err)
	}
	if !validBookmarkOverview(result) {
		return BookmarkOverview{}, errors.New("aggregate overview is invalid")
	}
	return result, nil
}

func validBookmarkOverview(result BookmarkOverview) bool {
	if result.Version != 1 || len(result.Views) != 6 {
		return false
	}
	for _, name := range []string{"all", "inbox", "kept", "compiled", "drop", "uncertain"} {
		if value, ok := result.Views[name]; !ok || value < 0 {
			return false
		}
	}
	c := result.Counts
	if c.Total < 0 || c.Pending < 0 || c.Processing < 0 || c.Completed < 0 ||
		c.Failed < 0 || c.Exhausted < 0 || c.Unsupported < 0 ||
		result.Views["all"] != c.Total ||
		result.Views["inbox"]+result.Views["kept"]+result.Views["compiled"]+result.Views["drop"] != c.Total ||
		result.Views["uncertain"] > c.Total ||
		c.Pending+c.Processing+c.Completed+c.Failed+c.Exhausted+c.Unsupported != c.Total ||
		result.Attention != c.Failed+c.Exhausted || result.Queued != c.Pending+c.Processing {
		return false
	}
	return true
}
