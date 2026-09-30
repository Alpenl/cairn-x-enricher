package enrich

import (
	"bytes"
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
	body, err := retrieveStoredResponseBody(ctx, baseURL, apiKey, responseID, httpClient)
	if err != nil {
		return StoredResponseSummary{}, err
	}
	return storedResponseSummary(body, responseID)
}

// RetrieveStoredSource keeps provider input/output in memory and returns only
// a source that passes the same search-evidence and field validation as an
// ordinary paid fetch. The caller must still verify the ledger-bound ID/model
// and let the Worker fence the original lease before any write.
func RetrieveStoredSource(ctx context.Context, baseURL, apiKey, responseID string,
	httpClient *http.Client) (StoredResponseSummary, Source, error) {
	body, err := retrieveStoredResponseBody(ctx, baseURL, apiKey, responseID, httpClient)
	if err != nil {
		return StoredResponseSummary{}, Source{}, err
	}
	summary, err := storedResponseSummary(body, responseID)
	if err != nil {
		return StoredResponseSummary{}, Source{}, err
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return StoredResponseSummary{}, Source{}, errors.New("invalid stored source payload")
	}
	source, err := decodeSource(envelope)
	if err != nil {
		return StoredResponseSummary{}, Source{}, fmt.Errorf("invalid stored source: %w", err)
	}
	return summary, source, nil
}

// RetrieveStoredReading validates a saved reading response against the current
// persisted source. The model is allowed to supply reading fields only; source
// text and links are copied from that source and verified by validateReading.
func RetrieveStoredReading(ctx context.Context, baseURL, apiKey, responseID string,
	source Source, httpClient *http.Client) (StoredResponseSummary, ReadingResult, error) {
	body, err := retrieveStoredResponseBody(ctx, baseURL, apiKey, responseID, httpClient)
	if err != nil {
		return StoredResponseSummary{}, ReadingResult{}, err
	}
	summary, err := storedResponseSummary(body, responseID)
	if err != nil {
		return StoredResponseSummary{}, ReadingResult{}, err
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return StoredResponseSummary{}, ReadingResult{}, errors.New("invalid stored reading payload")
	}
	reading, err := decodeReading(envelope, "")
	if err != nil {
		return StoredResponseSummary{}, ReadingResult{}, fmt.Errorf("invalid stored reading: %w", err)
	}
	_, err = validateReading(Input{SourceText: source.OriginalText}, Result{
		AITitle: reading.AITitle, OriginalLanguage: reading.OriginalLanguage,
		OriginalText: source.OriginalText, TranslatedText: reading.TranslatedText,
		Summary: reading.Summary, RelatedLinks: source.RelatedLinks,
		ImageURLs: source.ImageURLs, Model: reading.Model,
	})
	if err != nil {
		return StoredResponseSummary{}, ReadingResult{}, fmt.Errorf("invalid stored reading: %w", err)
	}
	return summary, reading, nil
}

func retrieveStoredResponseBody(ctx context.Context, baseURL, apiKey, responseID string,
	httpClient *http.Client) ([]byte, error) {
	if !storedResponseIDPattern.MatchString(responseID) {
		return nil, errors.New("invalid stored response ID")
	}
	if apiKey == "" {
		return nil, errors.New("provider API key is required")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
		base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("invalid provider base URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/responses/"+url.PathEscape(responseID), nil)
	if err != nil {
		return nil, fmt.Errorf("create stored response lookup: %w", err)
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
		return nil, fmt.Errorf("stored response lookup failed; billing remains unknown: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrStoredResponseNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stored response lookup HTTP %d; billing remains unknown", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelResponseBytes+1))
	if err != nil || len(body) > maxModelResponseBytes {
		return nil, errors.New("invalid stored response payload; billing remains unknown")
	}
	return body, nil
}

func storedResponseSummary(body []byte, responseID string) (StoredResponseSummary, error) {
	var wire struct {
		Object string `json:"object"`
		StoredResponseSummary
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
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
