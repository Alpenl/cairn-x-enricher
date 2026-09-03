package cairn

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
	"strconv"
	"strings"
)

const maxResponseBytes = 12 << 20

var imageKeyPattern = regexp.MustCompile(`^enrichment/[1-9][0-9]*/[0-9a-f]{64}\.(?:jpg|png|webp|gif|avif)$`)

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
	LeaseToken       string     `json:"lease_token"`
	AITitle          string     `json:"ai_title"`
	OriginalLanguage string     `json:"original_language"`
	OriginalText     string     `json:"original_text"`
	TranslatedText   string     `json:"translated_text"`
	Summary          string     `json:"summary"`
	RelatedLinks     []string   `json:"related_links"`
	Images           []ImageRef `json:"images"`
	Model            string     `json:"model"`
}

// ImageRef identifies one validated image stored in the Worker's R2 bucket.
type ImageRef struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

// Bookmark is the secret-free enrichment state shown in the management UI.
type Bookmark struct {
	ID               int64      `json:"id"`
	URL              string     `json:"url"`
	Note             string     `json:"note"`
	CreatedAt        string     `json:"created_at"`
	Status           string     `json:"status"`
	Processable      bool       `json:"processable,omitempty"`
	Attempts         int        `json:"attempts"`
	NextRetryAt      string     `json:"next_retry_at,omitempty"`
	AITitle          string     `json:"ai_title,omitempty"`
	OriginalLanguage string     `json:"original_language,omitempty"`
	OriginalText     string     `json:"original_text,omitempty"`
	TranslatedText   string     `json:"translated_text,omitempty"`
	Summary          string     `json:"summary,omitempty"`
	RelatedURLs      []string   `json:"related_links"`
	Images           []ImageRef `json:"images"`
	Model            string     `json:"model,omitempty"`
	Error            string     `json:"error,omitempty"`
	UpdatedAt        string     `json:"updated_at,omitempty"`
	EnrichedAt       string     `json:"enriched_at,omitempty"`
}

// BookmarkDetail preserves the detail endpoint's named response type.
type BookmarkDetail struct {
	Bookmark
}

// BookmarkCounts contains queue-wide counts for all bookmarks.
type BookmarkCounts struct {
	Total       int `json:"total"`
	Pending     int `json:"pending"`
	Processing  int `json:"processing"`
	Completed   int `json:"completed"`
	Failed      int `json:"failed"`
	Exhausted   int `json:"exhausted"`
	Unsupported int `json:"unsupported"`
}

// BookmarkPage is one newest-first page of bookmarks.
type BookmarkPage struct {
	Items        []Bookmark     `json:"items"`
	NextBeforeID *int64         `json:"next_before_id"`
	Counts       BookmarkCounts `json:"counts"`
}

// BookmarkQuery controls server-side filtering and pagination.
type BookmarkQuery struct {
	Limit    int
	BeforeID int64
	Status   string
	Search   string
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
	return decodeClaimResponse(response)
}

// ClaimByID atomically leases a selected X bookmark for a manual run.
func (c *Client) ClaimByID(ctx context.Context, id int64) (*Job, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	path := fmt.Sprintf("/api/enrichment/jobs/%d/claim", id)
	response, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	return decodeClaimResponse(response)
}

func decodeClaimResponse(response *http.Response) (*Job, error) {

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

// ListBookmarks returns a filtered newest-first page for the management UI.
func (c *Client) ListBookmarks(ctx context.Context, query BookmarkQuery) (BookmarkPage, error) {
	values := make(url.Values)
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	if query.BeforeID > 0 {
		values.Set("before_id", strconv.FormatInt(query.BeforeID, 10))
	}
	if query.Status != "" && query.Status != "all" {
		values.Set("status", query.Status)
	}
	if query.Search != "" {
		values.Set("q", query.Search)
	}
	path := "/api/enrichment/jobs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	response, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return BookmarkPage{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return BookmarkPage{}, apiError(response)
	}

	var page BookmarkPage
	if err := decodeJSON(response.Body, &page); err != nil {
		return BookmarkPage{}, fmt.Errorf("decode bookmark list: %w", err)
	}
	if page.Items == nil {
		page.Items = []Bookmark{}
	}
	for index := range page.Items {
		item := &page.Items[index]
		normalizeBookmarkCollections(item)
		if item.ID < 1 || item.URL == "" || !validBookmarkStatus(item.Status) || !validBookmarkImages(*item) {
			return BookmarkPage{}, errors.New("bookmark list contains an invalid item")
		}
	}
	return page, nil
}

// GetBookmark returns one X bookmark including its full source text.
func (c *Client) GetBookmark(ctx context.Context, id int64) (BookmarkDetail, error) {
	if id < 1 {
		return BookmarkDetail{}, errors.New("bookmark ID must be positive")
	}
	path := fmt.Sprintf("/api/enrichment/jobs/%d", id)
	response, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return BookmarkDetail{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return BookmarkDetail{}, apiError(response)
	}

	var detail BookmarkDetail
	if err := decodeJSON(response.Body, &detail); err != nil {
		return BookmarkDetail{}, fmt.Errorf("decode bookmark detail: %w", err)
	}
	if detail.ID < 1 || detail.URL == "" || !validBookmarkStatus(detail.Status) {
		return BookmarkDetail{}, errors.New("bookmark detail is invalid")
	}
	normalizeBookmarkCollections(&detail.Bookmark)
	if !validBookmarkImages(detail.Bookmark) {
		return BookmarkDetail{}, errors.New("bookmark detail contains an invalid image")
	}
	return detail, nil
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

// StoreImages asks the Worker to fetch validated X media and persist it in R2.
func (c *Client) StoreImages(ctx context.Context, id int64, leaseToken string, imageURLs []string) ([]ImageRef, error) {
	path := fmt.Sprintf("/api/enrichment/jobs/%d/images", id)
	response, err := c.do(ctx, http.MethodPost, path, struct {
		LeaseToken string   `json:"lease_token"`
		ImageURLs  []string `json:"image_urls"`
	}{LeaseToken: leaseToken, ImageURLs: imageURLs})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload struct {
		Images []ImageRef `json:"images"`
	}
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode stored images: %w", err)
	}
	if payload.Images == nil {
		payload.Images = []ImageRef{}
	}
	for _, image := range payload.Images {
		if !validImageRef(image) || !strings.HasPrefix(image.Key, fmt.Sprintf("enrichment/%d/", id)) {
			return nil, errors.New("stored image response contains an invalid object")
		}
	}
	return payload.Images, nil
}

// GetImage opens one R2-backed image response for the dashboard proxy.
func (c *Client) GetImage(ctx context.Context, key string) (*http.Response, error) {
	if !imageKeyPattern.MatchString(key) {
		return nil, errors.New("invalid image key")
	}
	segments := strings.Split(key, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/images/"+strings.Join(segments, "/"), nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer func() { _ = response.Body.Close() }()
		return nil, apiError(response)
	}
	return response, nil
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

func validBookmarkStatus(status string) bool {
	switch status {
	case "pending", "processing", "completed", "failed", "exhausted", "unsupported":
		return true
	default:
		return false
	}
}

func validImageRef(image ImageRef) bool {
	if !imageKeyPattern.MatchString(image.Key) {
		return false
	}
	switch image.ContentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/avif":
		return true
	default:
		return false
	}
}

func normalizeBookmarkCollections(bookmark *Bookmark) {
	if bookmark.RelatedURLs == nil {
		bookmark.RelatedURLs = []string{}
	}
	if bookmark.Images == nil {
		bookmark.Images = []ImageRef{}
	}
}

func validBookmarkImages(bookmark Bookmark) bool {
	prefix := fmt.Sprintf("enrichment/%d/", bookmark.ID)
	for _, image := range bookmark.Images {
		if !validImageRef(image) || !strings.HasPrefix(image.Key, prefix) {
			return false
		}
	}
	return true
}
