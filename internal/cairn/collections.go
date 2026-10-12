package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CollectionsProtocol is the collections contract this service speaks. v2 adds
// rule exclusions, review-before-add admission and confirmed-only rules; the
// paired Worker (deploy/stack-contract.json) answers with the same version.
const CollectionsProtocol = "2"

// CollectionsHeaderOK accepts the paired version. A v1 echo means an older
// Worker that cannot store v2 rule settings.
func CollectionsHeaderOK(value string) bool { return value == CollectionsProtocol }

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
	if !CollectionsHeaderOK(response.Header.Get("X-Cairn-Collections")) {
		return nil, &APIError{StatusCode: http.StatusConflict, Code: "collections_unsupported"}
	}
	var payload json.RawMessage
	err = decodeJSON(response.Body, &payload)
	return payload, err
}
