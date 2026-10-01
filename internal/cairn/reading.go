package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// ReadingSnapshot is the Worker's one-statement view of article text, effective
// tags and independent entity state. The nested CAS revisions belong to that
// same database read; callers must not stitch a newer revision onto old text.
type ReadingSnapshot struct {
	Version       int             `json:"version"`
	BodyUnchanged bool            `json:"body_unchanged"`
	Detail        BookmarkDetail  `json:"detail"`
	Selection     V2SelectionView `json:"selection"`
	Entities      json.RawMessage `json:"entities"`
}

// GetReading loads the combined Worker snapshot through the Enricher token.
// knownBodyRevision is optional; matching content is omitted from the response.
func (c *Client) GetReading(ctx context.Context, id int64, knownBodyRevision *int64) (ReadingSnapshot, error) {
	if id < 1 {
		return ReadingSnapshot{}, errors.New("bookmark ID must be positive")
	}
	path := fmt.Sprintf("/api/enrichment/jobs/%d/reading", id)
	if knownBodyRevision != nil {
		if *knownBodyRevision < 0 {
			return ReadingSnapshot{}, errors.New("body revision must not be negative")
		}
		path += fmt.Sprintf("?body_revision=%d", *knownBodyRevision)
	}
	response, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ReadingSnapshot{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return ReadingSnapshot{}, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return ReadingSnapshot{}, apiError(response)
	}
	// A legacy reading projection can omit effective resource/function tags.
	// Use the ordinary detail route rather than narrow a complete list item.
	if response.Header.Get("X-Cairn-Tag-System") != "1" ||
		response.Header.Get("X-Cairn-Content-Functions") != "1" {
		return ReadingSnapshot{}, ErrV2Unsupported
	}
	var reading ReadingSnapshot
	if err := decodeJSON(response.Body, &reading); err != nil {
		return ReadingSnapshot{}, fmt.Errorf("decode reading snapshot: %w", err)
	}
	identity := reading.Detail.CacheIdentity
	if reading.Version != 1 || reading.Detail.ID != id || reading.Detail.URL == "" ||
		!validBookmarkStatus(reading.Detail.Status) || identity == nil || identity.SchemaVersion != 1 ||
		identity.ContentRevision < 1 || identity.BodyRevision < 0 || identity.PersonalRevision < 0 ||
		identity.LatestDecisionID < 0 || identity.LatestEntityRevision < 0 ||
		reading.Selection.ID != id || reading.Selection.Revision != identity.PersonalRevision ||
		(reading.BodyUnchanged && (knownBodyRevision == nil || identity.BodyRevision != *knownBodyRevision)) ||
		len(reading.Entities) == 0 {
		return ReadingSnapshot{}, errors.New("reading snapshot is invalid")
	}
	var entity struct {
		ID       int64  `json:"id"`
		Revision int64  `json:"revision"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(reading.Entities, &entity); err != nil || entity.ID != id ||
		entity.Revision != identity.PersonalRevision || entity.State == "" {
		return ReadingSnapshot{}, errors.New("reading entity state is invalid")
	}
	selection := reading.Selection.Selection
	if selection.Topics == nil || selection.ResourceKinds == nil || selection.ContentFunctions == nil {
		return ReadingSnapshot{}, errors.New("reading snapshot is missing effective tag dimensions")
	}
	classification := reading.Detail.Classification
	if classification == nil {
		if len(selection.Topics)+len(selection.ResourceKinds)+len(selection.ContentFunctions) != 0 {
			return ReadingSnapshot{}, errors.New("reading detail is missing effective tags")
		}
	} else if !slices.Equal(classification.Topics, selection.Topics) ||
		!slices.Equal(classification.ResourceKinds, selection.ResourceKinds) ||
		!slices.Equal(classification.ContentFunctions, selection.ContentFunctions) {
		return ReadingSnapshot{}, errors.New("reading detail and selection tags differ")
	}
	normalizeBookmarkCollections(&reading.Detail.Bookmark)
	if !validBookmarkImages(reading.Detail.Bookmark) {
		return ReadingSnapshot{}, errors.New("reading detail contains an invalid image")
	}
	reading.Selection.Available = true
	return reading, nil
}
