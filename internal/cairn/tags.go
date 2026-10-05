package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CustomTag is a user-owned definition exposed with an effective bookmark.
// Its UUID and tag_ref carry identity independently of an editable label.
type CustomTag struct {
	ID       string `json:"id"`
	TagRef   string `json:"tag_ref"`
	Label    string `json:"label"`
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
	OwnerID  string `json:"owner_id"`
}

// GetV2TagSystem reads the negotiated tag-system API without guessing current
// provenance or historical event fields. The dashboard relays its JSON verbatim.
func (c *Client) GetV2TagSystem(ctx context.Context, path string) (json.RawMessage, error) {
	return c.tagSystemJSON(ctx, http.MethodGet, path, nil)
}

// MutateV2TagSystem forwards one explicit tag operation. The caller carries the
// operation identity and expected revision; this bridge never retries with a
// new identity or replaces a whole-selection snapshot.
func (c *Client) MutateV2TagSystem(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if method != http.MethodPost && method != http.MethodPatch && method != http.MethodDelete {
		return nil, errors.New("tag mutation requires POST, PATCH or DELETE")
	}
	return c.tagSystemJSON(ctx, method, path, body)
}

func (c *Client) tagSystemJSON(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/api/v2/") || strings.Contains(parsed.Path, "..") {
		return nil, errors.New("tag-system path must be a local v2 API path")
	}
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apiError(response)
	}
	if method == http.MethodGet && parsed.Query().Get("topic_refinements") != "" &&
		(response.Header.Get("X-Cairn-Topic-Granularity") != "1" || response.Header.Get("X-Cairn-Tag-System") != "1") {
		return nil, &APIError{StatusCode: http.StatusConflict, Code: "unsupported_topic_refinement_contract"}
	}
	if parsed.Query().Get("collection_id") != "" && response.Header.Get("X-Cairn-Collections") != "1" {
		return nil, &APIError{StatusCode: http.StatusConflict, Code: "collections_unsupported"}
	}
	if response.StatusCode == http.StatusNoContent {
		return json.RawMessage(`{}`), nil
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode tag-system response: %w", err)
	}
	return payload, nil
}
