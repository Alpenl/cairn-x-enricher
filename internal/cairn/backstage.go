package cairn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrBackstageUnsupported preserves the summary-list fallback for old Workers.
var ErrBackstageUnsupported = errors.New("backend does not support aggregate backstage")

// BookmarkBackstage is one database snapshot of attention rows and navigation.
type BookmarkBackstage struct {
	Version        int              `json:"version"`
	Attention      []Bookmark       `json:"attention"`
	AttentionTotal int              `json:"attention_total"`
	Counts         BookmarkCounts   `json:"counts"`
	Overview       BookmarkOverview `json:"overview"`
}

// GetBackstage reads the negotiated, bounded and body-free queue snapshot.
func (c *Client) GetBackstage(ctx context.Context) (BookmarkBackstage, error) {
	response, err := c.doWithHeaders(ctx, http.MethodGet, "/api/enrichment/backstage", nil,
		map[string]string{"X-Cairn-Backstage": "1"})
	if err != nil {
		return BookmarkBackstage{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return BookmarkBackstage{}, ErrBackstageUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return BookmarkBackstage{}, apiError(response)
	}
	if response.Header.Get("X-Cairn-Backstage") != "1" {
		return BookmarkBackstage{}, ErrBackstageUnsupported
	}
	var result BookmarkBackstage
	if err := decodeJSON(response.Body, &result); err != nil {
		return BookmarkBackstage{}, fmt.Errorf("decode backstage: %w", err)
	}
	if result.Version != 1 || !validBookmarkOverview(result.Overview) || result.Counts != result.Overview.Counts ||
		result.AttentionTotal != result.Overview.Attention || len(result.Attention) > 100 || len(result.Attention) > result.AttentionTotal {
		return BookmarkBackstage{}, errors.New("backstage snapshot is invalid")
	}
	seen := make(map[int64]bool)
	for index := range result.Attention {
		item := &result.Attention[index]
		if item.ID < 1 || item.URL == "" || seen[item.ID] || (item.Status != "failed" && item.Status != "exhausted") ||
			item.OriginalText != "" || item.TranslatedText != "" || !validBookmarkImages(*item) {
			return BookmarkBackstage{}, errors.New("backstage contains an invalid summary row")
		}
		seen[item.ID] = true
		normalizeBookmarkCollections(item)
	}
	if result.Attention == nil {
		result.Attention = []Bookmark{}
	}
	return result, nil
}
