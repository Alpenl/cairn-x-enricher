package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Collections forwards versioned user operations; retries preserve their identity.
func (c *Client) Collections(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || (parsed.Path != "/api/enrichment/collections" && !strings.HasPrefix(parsed.Path, "/api/enrichment/collections/")) || strings.Contains(parsed.Path, "..") || (method != http.MethodGet && method != http.MethodPost) {
		return nil, errors.New("invalid collections request")
	}
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apiError(response)
	}
	if response.Header.Get("X-Cairn-Collections") != "1" {
		return nil, &APIError{StatusCode: http.StatusConflict, Code: "collections_unsupported"}
	}
	var payload json.RawMessage
	err = decodeJSON(response.Body, &payload)
	return payload, err
}
