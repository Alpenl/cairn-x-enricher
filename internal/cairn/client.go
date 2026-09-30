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
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

const maxResponseBytes = 12 << 20

var imageKeyPattern = regexp.MustCompile(`^enrichment/[1-9][0-9]*/[0-9a-f]{64}\.(?:jpg|png|webp|gif|avif)$`)

// Job is one X bookmark leased from the Worker queue.
type Job struct {
	ID              int64  `json:"id"`
	URL             string `json:"url"`
	Note            string `json:"note"`
	CreatedAt       string `json:"created_at"`
	Attempt         int    `json:"attempt"`
	LeaseToken      string `json:"lease_token"`
	LeaseUntil      string `json:"lease_until"`
	ContentRevision int64  `json:"content_revision"`
	// RefreshEpoch is non-zero when the operator explicitly requested a source
	// refresh. The processor must then fetch the source instead of reusing the
	// stored snapshot (R2-06).
	RefreshEpoch int64 `json:"refresh_epoch,omitempty"`
	// SourceComponent is returned only to consumers that declare the staged
	// source gate capability. It is fixed when the lease is acquired.
	SourceComponent string `json:"source_component,omitempty"`
}

// Completion is the validated enrichment payload written back to Cairn Share.
type Completion struct {
	LeaseToken       string                   `json:"lease_token"`
	AITitle          string                   `json:"ai_title"`
	OriginalLanguage string                   `json:"original_language"`
	OriginalText     string                   `json:"original_text"`
	TranslatedText   string                   `json:"translated_text"`
	Summary          string                   `json:"summary"`
	RelatedLinks     []string                 `json:"related_links"`
	Images           []ImageRef               `json:"images"`
	Model            string                   `json:"model"`
	Classification   *taxonomy.Classification `json:"classification,omitempty"`
}

// ImageRef identifies one validated image stored in the Worker's R2 bucket.
type ImageRef struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

// Bookmark is the secret-free enrichment state shown in the management UI.
type Bookmark struct {
	CacheIdentity          *BookmarkCacheIdentity   `json:"cache_identity,omitempty"`
	ID                     int64                    `json:"id"`
	URL                    string                   `json:"url"`
	Note                   string                   `json:"note"`
	CreatedAt              string                   `json:"created_at"`
	Status                 string                   `json:"status"`
	Processable            bool                     `json:"processable,omitempty"`
	Attempts               int                      `json:"attempts"`
	NextRetryAt            string                   `json:"next_retry_at,omitempty"`
	AITitle                string                   `json:"ai_title,omitempty"`
	OriginalLanguage       string                   `json:"original_language,omitempty"`
	OriginalText           string                   `json:"original_text,omitempty"`
	TranslatedText         string                   `json:"translated_text,omitempty"`
	Summary                string                   `json:"summary,omitempty"`
	RelatedURLs            []string                 `json:"related_links"`
	Images                 []ImageRef               `json:"images"`
	Model                  string                   `json:"model,omitempty"`
	Error                  string                   `json:"error,omitempty"`
	PaidCallUnresolved     bool                     `json:"paid_call_unresolved"`
	PaidStage              string                   `json:"paid_stage,omitempty"`
	UpdatedAt              string                   `json:"updated_at,omitempty"`
	EnrichedAt             string                   `json:"enriched_at,omitempty"`
	Source                 string                   `json:"source,omitempty"`
	Why                    string                   `json:"why"`
	CurationStatus         string                   `json:"curation_status"`
	Classification         *taxonomy.Classification `json:"classification,omitempty"`
	CustomTags             []CustomTag              `json:"custom_tags,omitempty"`
	ClassificationReviewed bool                     `json:"classification_reviewed"`
	ContentLoaded          *bool                    `json:"content_loaded,omitempty"`
}

// BookmarkCacheIdentity is opt-in so strict legacy response readers are unchanged.
type BookmarkCacheIdentity struct {
	SchemaVersion        int   `json:"schema_version"`
	ContentRevision      int64 `json:"content_revision"`
	BodyRevision         int64 `json:"body_revision"`
	PersonalRevision     int64 `json:"personal_revision"`
	LatestDecisionID     int64 `json:"latest_decision_id"`
	LatestEntityRevision int64 `json:"latest_entity_revision"`
}

// BookmarkIdentity is the small, versioned response used while a detail stays visible.
type BookmarkIdentity struct {
	ID                 int64                 `json:"id"`
	Status             string                `json:"status"`
	UpdatedAt          string                `json:"updated_at"`
	PaidCallUnresolved bool                  `json:"paid_call_unresolved"`
	CacheIdentity      BookmarkCacheIdentity `json:"cache_identity"`
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
	FilterContractVersion *int           `json:"filter_contract_version,omitempty"`
	Items                 []Bookmark     `json:"items"`
	NextBeforeID          *int64         `json:"next_before_id"`
	Counts                BookmarkCounts `json:"counts"`
}

// BookmarkQuery controls server-side filtering and pagination.
type BookmarkQuery struct {
	IncludeCacheIdentity    bool
	Topics                  []string
	ResourceKinds           []string
	CustomTags              []string
	TopicMode               string
	ResourceMode            string
	CustomMode              string
	ContentFunctions        []string
	Carriers                []string
	Affordances             []string
	EntityStates            []string
	RequireEffectiveFilters bool
	Limit                   int
	BeforeID                int64
	Status                  string
	Search                  string
	CurationStatus          string
	Topic                   string
	Form                    string
	Use                     string
	Source                  string
	Uncertain               bool
	Since                   string
	SummaryOnly             bool
	SkipCounts              bool
}

// NeedsFilterContract rejects old backends that silently ignore v2 conditions.
func (q BookmarkQuery) NeedsFilterContract() bool {
	return q.RequireEffectiveFilters || len(q.Topics)+len(q.ResourceKinds)+len(q.CustomTags)+len(q.ContentFunctions)+len(q.Carriers)+len(q.Affordances)+len(q.EntityStates) > 0 || q.TopicMode != "" || q.ResourceMode != "" || q.CustomMode != ""
}

// NeedsTagFilterContract detects fields that an older effective-filter backend
// could silently ignore. Capability acknowledgement is required before using
// its result as a filtered page.
func (q BookmarkQuery) NeedsTagFilterContract() bool {
	return len(q.ResourceKinds)+len(q.CustomTags) > 0 || q.TopicMode != "" || q.ResourceMode != "" || q.CustomMode != ""
}

// CurationUpdate applies explicit human edits; a null classification restores AI suggestions.
type CurationUpdate struct {
	Why              *string         `json:"why,omitempty"`
	Status           *string         `json:"curation_status,omitempty"`
	Classification   json.RawMessage `json:"classification,omitempty"`
	ExpectedRevision *int64          `json:"expected_revision,omitempty"`
	OperationKey     *string         `json:"operation_key,omitempty"`
}

// APIError reports a stable error returned by the Cairn Share Worker. Revision
// carries the server's current revision for the conflict codes that expose one,
// so a UI can explain a CAS conflict instead of showing a generic backend error
// (F04).
type APIError struct {
	StatusCode int
	Code       string
	Revision   *int64
	RetryAfter string
}

func (e *APIError) Error() string {
	base := fmt.Sprintf("cairn API returned HTTP %d", e.StatusCode)
	if e.Code == "" {
		return base
	}
	if e.Revision != nil {
		return fmt.Sprintf("%s (%s, revision %d)", base, e.Code, *e.Revision)
	}
	return fmt.Sprintf("%s (%s)", base, e.Code)
}

// IsConflict reports whether the error is an actionable optimistic-concurrency
// conflict rather than an internal failure.
func (e *APIError) IsConflict() bool {
	switch e.Code {
	case "revision_conflict", "snapshot_conflict", "hidden_value_conflict", "spec_conflict", "operation_conflict":
		return true
	}
	return false
}

// Class maps a Worker error to the shared runtime class. The Worker returns a
// typed code rather than a bare status precisely so the consumer does not have
// to guess: a 409 may be a lost lease, a changed target, changed input or a
// duplicate completion, and each needs different recovery.
func (e *APIError) Class() enrich.ErrorClass {
	switch e.Code {
	case "budget_exhausted":
		return enrich.ErrorClassBudget
	case "capability_mismatch", "configuration_error":
		return enrich.ErrorClassConfiguration
	case "invalid_classification", "invalid_classification_config", "invalid_source", "invalid_operation_key", "invalid_json":
		return enrich.ErrorClassContract
	case "target_changed", "input_changed", "lease_expired", "lease_released", "lease_conflict", "revision_conflict", "snapshot_conflict", "hidden_value_conflict", "run_stale":
		return enrich.ErrorClassStale
	case "already_completed":
		return enrich.ErrorClassCompleted
	case "operation_conflict", "spec_conflict", "spec_hash_mismatch", "invalid_override", "invalid_automatic", "run_spec_mismatch", "run_model_mismatch", "run_not_succeeded", "unknown_run":
		return enrich.ErrorClassContract
	}
	if e.StatusCode >= http.StatusInternalServerError && e.StatusCode <= 599 {
		return enrich.ErrorClassTransient
	}
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		return enrich.ErrorClassConfiguration
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusUnsupportedMediaType:
		return enrich.ErrorClassContract
	case http.StatusConflict:
		// A bare 409 has no proof that this job was superseded. Typed Worker
		// reason codes above decide whether the conflict is stale or fatal.
		return enrich.ErrorClassContract
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return enrich.ErrorClassTransient
	default:
		return enrich.ErrorClassUnknown
	}
}

// Client calls the Cairn Share Worker's internal enrichment endpoints.
type Client struct {
	baseURL              string
	token                string
	httpClient           *http.Client
	classificationBudget *classify.CallBudgetLimits
	specMu               sync.RWMutex
	registeredSpecHashes map[string]string
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

// ClaimAllowed excludes an instance's locally paused paid stage before the
// Worker takes a lease. Other instances may still claim that stage.
func (c *Client) ClaimAllowed(ctx context.Context, source, reading bool) (*Job, error) {
	if !source && !reading {
		return nil, nil
	}
	mask := "both"
	if !source {
		mask = "reading"
	} else if !reading {
		mask = "source"
	}
	response, err := c.doWithHeaders(ctx, http.MethodPost, "/api/enrichment/jobs/claim", nil,
		map[string]string{"X-Cairn-Source-Stage-Pause": "1", "X-Cairn-Source-Stage-Mask": mask})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	job, err := decodeClaimResponse(response)
	if err != nil || job == nil {
		return job, err
	}
	if job.SourceComponent != "source" && job.SourceComponent != "reading" ||
		job.SourceComponent == "source" && !source || job.SourceComponent == "reading" && !reading {
		return nil, errors.New("source claim returned an excluded or unknown component")
	}
	return job, nil
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

// VerifySourceLeaseCapability prevents a new consumer from draining attempts
// against an older Worker that cannot fence paid calls or atomically consume
// a refresh intent with its saved source checkpoint.
func (c *Client) VerifySourceLeaseCapability(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/source-lease-capability", nil)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("source lease admission is unavailable: %w", apiError(response))
	}
	var capability struct {
		Protocol              int  `json:"protocol"`
		LeaseMS               int  `json:"lease_ms"`
		PaidStageAdmission    bool `json:"paid_stage_admission"`
		ProviderResultGuard   bool `json:"provider_result_guard"`
		CompletionReplay      bool `json:"completion_replay"`
		ProviderAttemptLedger bool `json:"provider_attempt_ledger"`
		RefreshCheckpoint     bool `json:"refresh_source_checkpoint"`
		SourceComponentGate   bool `json:"source_component_gate"`
		SourceStagePause      bool `json:"source_stage_pause"`
	}
	if err := decodeJSON(response.Body, &capability); err != nil {
		return fmt.Errorf("decode source lease capability: %w", err)
	}
	if capability.Protocol != 1 || capability.LeaseMS != int((15*time.Minute).Milliseconds()) ||
		!capability.PaidStageAdmission || !capability.ProviderResultGuard || !capability.CompletionReplay ||
		!capability.ProviderAttemptLedger || !capability.RefreshCheckpoint ||
		!capability.SourceComponentGate || !capability.SourceStagePause {
		return errors.New("source lease admission protocol is incompatible")
	}
	return nil
}

// SourceClaimable reports whether a scheduled source claim would find work at
// this instant. It does not acquire a lease. A one-shot caller that sees false
// must skip source claiming for that invocation: new work can arrive after the
// check, and it must never run without the reading contract canary.
func (c *Client) SourceClaimable(ctx context.Context) (bool, error) {
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/source-claimable", nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false, apiError(response)
	}
	var result struct {
		Claimable *bool `json:"claimable"`
	}
	if err := decodeJSON(response.Body, &result); err != nil {
		return false, fmt.Errorf("decode source claimability: %w", err)
	}
	if result.Claimable == nil {
		return false, errors.New("source claimability response omitted claimable")
	}
	return *result.Claimable, nil
}

// AdmitSourceStage checks the authoritative lease immediately before a paid
// call. A short lease is conditionally released by the Worker; a claim with no
// previous paid-stage admission has its attempt refunded there.
func (c *Client) AdmitSourceStage(ctx context.Context, id int64, leaseToken, stage string, minRemaining time.Duration) error {
	if id < 1 || leaseToken == "" || (stage != "fetch" && stage != "reading") ||
		minRemaining <= 0 || minRemaining > 15*time.Minute {
		return errors.New("invalid source stage admission")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/enrichment/jobs/%d/lease-admit", id),
		map[string]any{"lease_token": leaseToken, "stage": stage, "min_remaining_ms": minRemaining.Milliseconds()})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var receipt struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		RemainingMS int64  `json:"remaining_ms"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return fmt.Errorf("decode source stage admission: %w", err)
	}
	if receipt.ID != id || receipt.Status != "admitted" || receipt.RemainingMS <= 0 {
		return errors.New("source stage admission receipt is invalid")
	}
	return nil
}

// RequestEnrichment persists a priority request. The scheduler will claim it
// after it has execution capacity, so no lease waits in an HTTP handler queue.
func (c *Client) RequestEnrichment(ctx context.Context, id int64, operationKey string) error {
	if id < 1 || operationKey == "" || len(operationKey) > 200 {
		return errors.New("invalid manual request")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/enrichment/jobs/%d/enqueue", id),
		map[string]any{"operation_key": operationKey})
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var receipt struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Action string `json:"action"`
	}
	if err := decodeJSON(response.Body, &receipt); err != nil {
		return fmt.Errorf("decode manual queue receipt: %w", err)
	}
	if receipt.ID != id || receipt.Status != "pending" {
		return errors.New("manual queue receipt is invalid")
	}
	return nil
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
	if job.ID < 1 || job.URL == "" || job.Attempt < 1 || job.LeaseToken == "" ||
		job.LeaseUntil == "" || job.ContentRevision < 1 {
		return nil, errors.New("claim response is missing required fields")
	}
	return &job, nil
}

// ListBookmarks returns a filtered newest-first page for the management UI.
func (c *Client) ListBookmarks(ctx context.Context, query BookmarkQuery) (BookmarkPage, error) {
	values := make(url.Values)
	if query.SkipCounts {
		values.Set("counts", "0")
	}
	if query.IncludeCacheIdentity {
		values.Set("include_cache_identity", "1")
	}
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
	for key, value := range map[string]string{
		"curation_status": query.CurationStatus, "topic": query.Topic, "form": query.Form,
		"use": query.Use, "source": query.Source, "since": query.Since,
		"topics_mode": query.TopicMode, "resource_mode": query.ResourceMode, "custom_mode": query.CustomMode,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	for key, terms := range map[string][]string{
		"topics": query.Topics, "content_functions": query.ContentFunctions, "carriers": query.Carriers,
		"affordances": query.Affordances, "entity_state": query.EntityStates,
		"resource_kinds": query.ResourceKinds, "custom_tags": query.CustomTags,
	} {
		if len(terms) > 0 {
			values.Set(key, strings.Join(terms, ","))
		}
	}
	if query.NeedsFilterContract() {
		values.Set("filter_contract_version", "1")
	}
	if query.Uncertain {
		values.Set("uncertain", "true")
	}
	if query.SummaryOnly {
		values.Set("view", "summary")
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
	if query.NeedsTagFilterContract() && response.Header.Get("X-Cairn-Tag-System") != "1" {
		return BookmarkPage{}, &APIError{StatusCode: http.StatusConflict, Code: "unsupported_tag_filter_contract"}
	}

	var page BookmarkPage
	if err := decodeJSON(response.Body, &page); err != nil {
		return BookmarkPage{}, fmt.Errorf("decode bookmark list: %w", err)
	}
	if query.NeedsFilterContract() && (page.FilterContractVersion == nil || *page.FilterContractVersion != 1) {
		return BookmarkPage{}, &APIError{StatusCode: http.StatusConflict, Code: "unsupported_filter_contract"}
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
	path := fmt.Sprintf("/api/enrichment/jobs/%d?include_cache_identity=1", id)
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
	if detail.ID != id || detail.URL == "" || !validBookmarkStatus(detail.Status) {
		return BookmarkDetail{}, errors.New("bookmark detail is invalid")
	}
	normalizeBookmarkCollections(&detail.Bookmark)
	if !validBookmarkImages(detail.Bookmark) {
		return BookmarkDetail{}, errors.New("bookmark detail contains an invalid image")
	}
	return detail, nil
}

// GetBookmarkIdentity avoids transferring the article body on idle checks.
func (c *Client) GetBookmarkIdentity(ctx context.Context, id int64) (BookmarkIdentity, error) {
	if id < 1 {
		return BookmarkIdentity{}, errors.New("bookmark ID must be positive")
	}
	path := fmt.Sprintf("/api/enrichment/jobs/%d/cache-identity", id)
	response, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return BookmarkIdentity{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return BookmarkIdentity{}, apiError(response)
	}
	var identity BookmarkIdentity
	if err := decodeJSON(response.Body, &identity); err != nil {
		return BookmarkIdentity{}, fmt.Errorf("decode bookmark identity: %w", err)
	}
	if identity.ID != id || !validBookmarkStatus(identity.Status) ||
		identity.CacheIdentity.SchemaVersion != 1 || identity.CacheIdentity.ContentRevision < 1 ||
		identity.CacheIdentity.BodyRevision < 0 || identity.CacheIdentity.PersonalRevision < 0 ||
		identity.CacheIdentity.LatestDecisionID < 0 || identity.CacheIdentity.LatestEntityRevision < 0 {
		return BookmarkIdentity{}, errors.New("bookmark identity is invalid")
	}
	return identity, nil
}

// GetTaxonomy loads the Worker's single authoritative vocabulary.
func (c *Client) GetTaxonomy(ctx context.Context) (taxonomy.Catalog, error) {
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/taxonomy", nil)
	if err != nil {
		return taxonomy.Catalog{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return taxonomy.Catalog{}, apiError(response)
	}
	var catalog taxonomy.Catalog
	if err := decodeJSON(response.Body, &catalog); err != nil {
		return taxonomy.Catalog{}, fmt.Errorf("decode taxonomy: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return taxonomy.Catalog{}, err
	}
	return catalog, nil
}

// UpdateCuration persists human organization without claiming or re-enriching a link.
func (c *Client) UpdateCuration(ctx context.Context, id int64, update CurationUpdate) (BookmarkDetail, error) {
	if id < 1 {
		return BookmarkDetail{}, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/enrichment/jobs/%d/curation", id), update)
	if err != nil {
		return BookmarkDetail{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return BookmarkDetail{}, apiError(response)
	}
	var detail BookmarkDetail
	if err := decodeJSON(response.Body, &detail); err != nil {
		return BookmarkDetail{}, fmt.Errorf("decode curation result: %w", err)
	}
	if detail.ID != id || detail.URL == "" || !validBookmarkStatus(detail.Status) || !validBookmarkImages(detail.Bookmark) {
		return BookmarkDetail{}, errors.New("curation response is invalid")
	}
	normalizeBookmarkCollections(&detail.Bookmark)
	return detail, nil
}

// Complete commits a successful result while the supplied lease is current.
func (c *Client) Complete(ctx context.Context, id int64, completion Completion) error {
	path := fmt.Sprintf("/api/enrichment/jobs/%d/complete", id)
	return c.retryExactStageWrite(ctx, path, completion)
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
	id, err := strconv.ParseInt(segments[1], 10, 64)
	if err != nil || id < 1 {
		return nil, errors.New("invalid image owner")
	}
	// Older Workers can retain and serve orphan R2 objects. Validate the
	// authoritative bookmark before fetching and again before exposing the body.
	// Never cache this check: a prior successful read is not deletion authority.
	if _, err := c.GetBookmark(ctx, id); err != nil {
		return nil, err
	}
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
	if _, err := c.GetBookmark(ctx, id); err != nil {
		_ = response.Body.Close()
		return nil, err
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

// FailSourceStage reports a provider transient with its stage so the Worker
// can pause only that stage in the same transaction as the leased job failure.
// A content or contract failure keeps the existing per-job fail contract.
func (c *Client) FailSourceStage(ctx context.Context, id int64, leaseToken, stage, message string,
	retryAfter time.Duration, providerTransient bool) error {
	if stage != "fetch" && stage != "reading" {
		return errors.New("invalid source failure stage")
	}
	if !providerTransient {
		return c.Fail(ctx, id, leaseToken, message)
	}
	fault := "source_transient"
	if stage == "reading" {
		fault = "reading_transient"
	}
	path := fmt.Sprintf("/api/enrichment/jobs/%d/fail", id)
	response, err := c.do(ctx, http.MethodPost, path, map[string]any{
		"lease_token": leaseToken, "error": message, "component_fault": fault,
		"retry_after_ms": min(max(retryAfter, 0), 10*time.Minute).Milliseconds(),
	})
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
	return c.doWithHeaders(ctx, method, path, body, nil)
}

func (c *Client) doWithHeaders(ctx context.Context, method, path string, body any,
	extraHeaders map[string]string) (*http.Response, error) {
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
	// This client understands the additive tag-system contract. Older clients
	// omit the header, allowing the Worker to keep strict legacy responses legal.
	request.Header.Set("X-Cairn-Tag-System", "1")
	request.Header.Set("X-Cairn-Content-Functions", "1")
	if path == "/api/enrichment/jobs/claim" ||
		(strings.HasPrefix(path, "/api/enrichment/jobs/") && strings.HasSuffix(path, "/claim")) {
		request.Header.Set("X-Cairn-Source-Lease-Admission", "1")
		request.Header.Set("X-Cairn-Source-Component-Gate", "1")
		request.Header.Set("X-Cairn-Provider-Attempt-Ledger", "1")
	}
	if path == "/api/enrichment/source-claimable" {
		request.Header.Set("X-Cairn-Source-Component-Gate", "1")
	}
	if strings.HasSuffix(path, "/lease-admit") || strings.HasSuffix(path, "/budget-defer") ||
		strings.HasSuffix(path, "/local-defer") {
		request.Header.Set("X-Cairn-Provider-Attempt-Ledger", "1")
	}
	if strings.HasPrefix(path, "/api/enrichment/classifications/") {
		request.Header.Set("X-Cairn-Classification-Budget", "1")
	}
	if path == "/api/enrichment/classifications/claim" {
		request.Header.Set("X-Cairn-Classification-Gate", "1")
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range extraHeaders {
		request.Header.Set(name, value)
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
		Code     string `json:"error"`
		Revision *int64 `json:"revision"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 8<<10)).Decode(&payload)
	return &APIError{StatusCode: response.StatusCode, Code: payload.Code, Revision: payload.Revision,
		RetryAfter: response.Header.Get("Retry-After")}
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
	if bookmark.CurationStatus == "" {
		bookmark.CurationStatus = "inbox"
	}
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
