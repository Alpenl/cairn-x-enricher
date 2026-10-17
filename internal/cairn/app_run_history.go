package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"
)

// ErrRunHistoryUnavailable means a known history service no longer has this
// record. It must not be displayed as an empty successful judgment.
var ErrRunHistoryUnavailable = errors.New("stored run is unavailable")

// GetAppRunHistory reads one summary page or one explicitly chosen full run.
// Raw JSON retains historical null/missing metadata; today's model or policy
// configuration is never substituted. There is no legacy all-runs fallback.
func (c *Client) GetAppRunHistory(ctx context.Context, id, runID, after int64) (json.RawMessage, error) {
	const maxID = 1<<53 - 1
	if id < 1 || id > maxID || runID < 0 || runID > maxID || after < 0 || after > maxID || runID > 0 && after > 0 {
		return nil, errors.New("invalid run history reference")
	}
	path := fmt.Sprintf("/api/v2/links/%d/run-history", id)
	limit := 128 << 10
	if runID > 0 {
		path += fmt.Sprintf("/%d", runID)
		// Archived payloads are capped at 4 MiB; leave room for row metadata.
		limit = 5 << 20
	} else {
		if after > 0 {
			path += fmt.Sprintf("?after_id=%d", after)
		}
	}
	response, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, map[string]string{"X-Cairn-App-Run-History": "1"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	supported := response.Header.Get("X-Cairn-App-Run-History") == "1"
	switch {
	case response.StatusCode == 405 || response.StatusCode == 501 || response.StatusCode == 404 && !supported:
		return nil, ErrV2Unsupported
	case supported && (response.StatusCode == 404 || response.StatusCode == 410):
		return nil, ErrRunHistoryUnavailable
	case response.StatusCode != http.StatusOK:
		return nil, apiError(response)
	case !supported:
		return nil, ErrV2Unsupported
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	invalid := errors.New("invalid run history response")
	if len(payload) > limit || !utf8.Valid(payload) {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return nil, invalid
	}
	positive := func(raw json.RawMessage) int64 {
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 1 || value > maxID {
			return 0
		}
		return value
	}
	if positive(fields["version"]) != 1 {
		return nil, ErrV2Unsupported
	}
	if positive(fields["bookmark_id"]) != id {
		return nil, invalid
	}
	if runID > 0 {
		var run map[string]json.RawMessage
		if json.Unmarshal(fields["run"], &run) != nil || positive(run["id"]) != runID {
			return nil, invalid
		}
	} else {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(fields["runs"], &rows) != nil || rows == nil || len(rows) > 50 {
			return nil, invalid
		}
		previous := after
		for _, row := range rows {
			current := positive(row["id"])
			if current == 0 || previous > 0 && current >= previous {
				return nil, invalid
			}
			for _, key := range []string{"policy", "answers", "usage", "raw_judgments", "operation_key"} {
				if _, exists := row[key]; exists {
					return nil, invalid
				}
			}
			previous = current
		}
		next := fields["next_after_id"]
		if len(next) == 0 || string(next) != "null" && (len(rows) == 0 || positive(next) != previous) {
			return nil, invalid
		}
	}
	return json.RawMessage(payload), nil
}
