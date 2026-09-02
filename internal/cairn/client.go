package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxResponseBytes = 1 << 20

// Job is one X bookmark leased from the Worker queue.
type Job struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Note       string `json:"note"`
	CreatedAt  string `json:"created_at"`
	Attempt    int    `json:"attempt"`
	LeaseToken string `json:"lease_token"`
	LeaseUntil string `json:"lease_until"`
}

// Completion is the validated enrichment payload written back to Cairn Share.
type Completion struct {
	LeaseToken   string   `json:"lease_token"`
	OriginalText string   `json:"original_text"`
	Summary      string   `json:"summary"`
	RelatedLinks []string `json:"related_links"`
	Model        string   `json:"model"`
}

// APIError reports a stable error returned by the Cairn Share Worker.
type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("cairn API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("cairn API returned HTTP %d (%s)", e.StatusCode, e.Code)
}

// Client calls the Cairn Share Worker's internal enrichment endpoints.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a client for the Worker's internal enrichment API.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: httpClient,
	}
}

// Claim atomically leases the next eligible X bookmark, or returns nil when empty.
func (c *Client) Claim(ctx context.Context) (*Job, error) {
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/jobs/claim", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}

	var job Job
	if err := decodeJSON(response.Body, &job); err != nil {
		return nil, fmt.Errorf("decode claim response: %w", err)
	}
	if job.ID < 1 || job.URL == "" || job.Attempt < 1 || job.LeaseToken == "" || job.LeaseUntil == "" {
		return nil, errors.New("claim response is missing required fields")
	}
	return &job, nil
}

// Complete commits a successful result while the supplied lease is current.
func (c *Client) Complete(ctx context.Context, id int64, completion Completion) error {
	path := fmt.Sprintf("/api/enrichment/jobs/%d/complete", id)
	response, err := c.do(ctx, http.MethodPost, path, completion)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	return nil
}

// Fail records an attempt failure while the supplied lease is current.
func (c *Client) Fail(ctx context.Context, id int64, leaseToken, message string) error {
	path := fmt.Sprintf("/api/enrichment/jobs/%d/fail", id)
	response, err := c.do(ctx, http.MethodPost, path, struct {
		LeaseToken string `json:"lease_token"`
		Error      string `json:"error"`
	}{LeaseToken: leaseToken, Error: message})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call cairn API: %w", err)
	}
	return response, nil
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing JSON data")
	}
	return nil
}

func apiError(response *http.Response) error {
	var payload struct {
		Code string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 8<<10)).Decode(&payload)
	return &APIError{StatusCode: response.StatusCode, Code: payload.Code}
}
