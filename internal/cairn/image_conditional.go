package cairn

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GetImageConditional validates a private image on every read. A negotiated
// Worker owns both authorization and the post-R2 deletion check; old Workers
// retain the original owner checks before and after the image request.
func (c *Client) GetImageConditional(ctx context.Context, key, etag string) (*http.Response, error) {
	if !imageKeyPattern.MatchString(key) {
		return nil, errors.New("invalid image key")
	}
	parts := strings.Split(key, "/")
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		return nil, errors.New("invalid image owner")
	}
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	path := "/api/enrichment/images/" + strings.Join(parts, "/")
	headers := map[string]string{"X-Cairn-Image-Privacy": "1"}
	if etag != "" {
		if !validImageETag(etag) {
			return nil, errors.New("invalid image validator")
		}
		headers["If-None-Match"] = etag
	}
	prechecked := !c.imagePrivacyVerified.Load()
	if prechecked {
		if _, err := c.GetBookmark(ctx, id); err != nil {
			return nil, err
		}
	}
	response, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, headers)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		defer func() { _ = response.Body.Close() }()
		return nil, apiError(response)
	}
	if response.Header.Get("X-Cairn-Image-Privacy") == "1" {
		if response.StatusCode == http.StatusNotModified &&
			(etag == "" || response.Header.Get("ETag") != etag) {
			_ = response.Body.Close()
			return nil, errors.New("image validator response is invalid")
		}
		c.imagePrivacyVerified.Store(true)
		return response, nil
	}
	c.imagePrivacyVerified.Store(false)
	// A rollback can remove a previously verified capability. Never expose its
	// unacknowledged response: repeat the old read with both owner checks.
	if !prechecked {
		_ = response.Body.Close()
		if _, err := c.GetBookmark(ctx, id); err != nil {
			return nil, err
		}
		response, err = c.doWithHeaders(ctx, http.MethodGet, path, nil, headers)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
			defer func() { _ = response.Body.Close() }()
			return nil, apiError(response)
		}
	}
	if _, err := c.GetBookmark(ctx, id); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	if response.StatusCode == http.StatusNotModified && (etag == "" || response.Header.Get("ETag") != etag) {
		_ = response.Body.Close()
		return nil, errors.New("image validator response is invalid")
	}
	return response, nil
}

func validImageETag(etag string) bool {
	return len(etag) >= 2 && len(etag) <= 256 && etag[0] == '"' && etag[len(etag)-1] == '"' &&
		!strings.ContainsAny(etag[1:len(etag)-1], "\"\r\n")
}
