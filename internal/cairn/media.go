package cairn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var mediaIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Media returns the current capture’s archived media descriptors.
func (c *Client) Media(ctx context.Context, id int64) (json.RawMessage, error) {
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/enrichment/%d/media", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var result json.RawMessage
	err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result)
	return result, err
}

// MediaFile streams an owned media file with optional byte range.
func (c *Client) MediaFile(ctx context.Context, id, byteRange string) (*http.Response, error) {
	if !mediaIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid media ID")
	}
	client := NewClient(c.baseURL, c.token, &http.Client{Transport: c.httpClient.Transport, CheckRedirect: c.httpClient.CheckRedirect, Jar: c.httpClient.Jar, Timeout: 5 * time.Minute})
	return client.doWithHeaders(ctx, http.MethodGet, "/api/enrichment/media/"+id, nil, map[string]string{"Range": byteRange})
}
