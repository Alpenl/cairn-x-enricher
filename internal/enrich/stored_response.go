package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrStoredResponseNotFound keeps a provider 404 distinct from billing proof.
var ErrStoredResponseNotFound = errors.New("stored provider response not found; billing remains unknown")

var storedResponseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// StoredResponseSummary deliberately omits provider input and output. A
// response lookup is evidence for an operator, not authority to unblock a job.
type StoredResponseSummary struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Model     string `json:"model"`
	CreatedAt *int64 `json:"created_at"`
	Usage     struct {
		InputTokens         *int64 `json:"input_tokens"`
		OutputTokens        *int64 `json:"output_tokens"`
		TotalTokens         *int64 `json:"total_tokens"`
		CostUSDTicks        *int64 `json:"cost_in_usd_ticks"`
		ServerSideToolUsage struct {
			XSearchCalls *int64 `json:"x_search_calls"`
		} `json:"server_side_tool_usage_details"`
	} `json:"usage"`
}

// RetrieveStoredResponse performs only the documented xAI GET. The caller
// must independently bind the supplied ID to the intended ledger operation;
// a 404, timeout or malformed reply never proves that no charge occurred.
func RetrieveStoredResponse(ctx context.Context, baseURL, apiKey, responseID string,
	httpClient *http.Client) (StoredResponseSummary, error) {
	if !storedResponseIDPattern.MatchString(responseID) {
		return StoredResponseSummary{}, errors.New("invalid stored response ID")
	}
	if apiKey == "" {
		return StoredResponseSummary{}, errors.New("provider API key is required")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return StoredResponseSummary{}, errors.New("invalid provider base URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/responses/"+url.PathEscape(responseID), nil)
	if err != nil {
		return StoredResponseSummary{}, fmt.Errorf("create stored response lookup: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	if httpClient != nil {
		client = httpClient
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(request)
	if err != nil {
		return StoredResponseSummary{}, fmt.Errorf("stored response lookup failed; billing remains unknown: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return StoredResponseSummary{}, ErrStoredResponseNotFound
	}
	if response.StatusCode != http.StatusOK {
		return StoredResponseSummary{}, fmt.Errorf("stored response lookup HTTP %d; billing remains unknown", response.StatusCode)
	}
	var wire struct {
		Object string `json:"object"`
		StoredResponseSummary
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxModelResponseBytes+1))
	if err := decoder.Decode(&wire); err != nil {
		return StoredResponseSummary{}, errors.New("invalid stored response payload; billing remains unknown")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return StoredResponseSummary{}, errors.New("invalid stored response payload; billing remains unknown")
	}
	if wire.Object != "response" || wire.ID != responseID || wire.Model == "" || wire.Status == "" {
		return StoredResponseSummary{}, errors.New("stored response identity mismatch; billing remains unknown")
	}
	return wire.StoredResponseSummary, nil
}
