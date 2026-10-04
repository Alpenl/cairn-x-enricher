package cairn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Presentation reads or queues a separate reading presentation.
func (c *Client) Presentation(ctx context.Context, id int64, method string, force bool) (json.RawMessage, error) {
	response, err := c.do(ctx, method, fmt.Sprintf("/api/enrichment/%d/presentation", id), map[string]any{"force": force})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return nil, apiError(response)
	}
	var result json.RawMessage
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}
