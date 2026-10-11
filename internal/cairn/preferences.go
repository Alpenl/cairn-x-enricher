package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// Preferences forwards the cross-device preference and legacy-count endpoints.
// Only these exact internal paths are allowed.
func (c *Client) Preferences(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	allowed := map[string]string{
		"/api/enrichment/preferences":            http.MethodGet,
		"/api/enrichment/preferences/operations": http.MethodPost,
		"/api/enrichment/links/legacy-dropped":   http.MethodGet,
	}
	if want, ok := allowed[path]; !ok || want != method {
		return nil, errors.New("invalid preferences request")
	}
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	err = decodeJSON(response.Body, &payload)
	return payload, err
}
