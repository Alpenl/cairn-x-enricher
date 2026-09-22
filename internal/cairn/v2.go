package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// V2Selection is the multidimensional selection exposed by the Worker's v2
// API. It is a superset of the v1 six-field projection: the additional
// dimensions are additive, so a v1-only Worker simply does not return them.
type V2Selection struct {
	Topics           []string `json:"topics"`
	ContentFunctions []string `json:"content_functions"`
	Carriers         []string `json:"carriers"`
	Affordances      []string `json:"affordances"`
	Form             string   `json:"form"`
	Use              string   `json:"use"`
}

// V2SelectionView is the response shape for a selection read. It carries every
// field the Worker actually returns, in both the fallback (why/curation_status)
// and the v2 (definition_version/provenance/revised_at) shapes: a strict
// decoder that rejects them turns a working backend into a 502 and hides the
// panel (F03).
type V2SelectionView struct {
	ID                int64           `json:"id"`
	Selection         V2Selection     `json:"selection"`
	Automatic         *V2Selection    `json:"automatic,omitempty"`
	TaxonomyVersion   string          `json:"taxonomy_version,omitempty"`
	DefinitionVersion int             `json:"definition_version,omitempty"`
	Provenance        json.RawMessage `json:"provenance,omitempty"`
	RevisedAt         string          `json:"revised_at,omitempty"`
	V1Only            bool            `json:"v1_only,omitempty"`
	Revision          int64           `json:"revision"`
	Available         bool            `json:"-"`
	V1Projection      V2Selection     `json:"v1_projection,omitempty"`
	// Empty records which dimensions the human explicitly set empty.
	Empty          json.RawMessage `json:"empty,omitempty"`
	Why            string          `json:"why,omitempty"`
	CurationStatus string          `json:"curation_status,omitempty"`
}

// V2SelectionUpdate is the whole-selection write. The operation identity and
// expected revision travel with it so a retried write is idempotent and a stale
// client gets an actionable conflict instead of a silent last-write-wins.
type V2SelectionUpdate struct {
	V2Selection
	OperationKey     string `json:"operation_key,omitempty"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

// ErrV2Unsupported reports that the Worker does not implement the v2 API. It is
// distinct from an empty selection so the UI can degrade to read-only instead
// of showing an empty record.
var ErrV2Unsupported = errors.New("backend does not support the v2 selection API")

// IsUnsupported reports whether an error means the backend simply does not
// implement the v2 endpoint. Callers use it to fall back to the legacy path
// instead of treating a missing route as a data error.
func IsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrV2Unsupported) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusMethodNotAllowed {
			return true
		}
	}
	return false
}

// GetV2Selection reads the multidimensional selection for a link.
func (c *Client) GetV2Selection(ctx context.Context, id int64) (V2SelectionView, error) {
	if id < 1 {
		return V2SelectionView{}, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/selection?include_automatic=1", id), nil)
	if err != nil {
		return V2SelectionView{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return V2SelectionView{}, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return V2SelectionView{}, apiError(response)
	}
	var view V2SelectionView
	if err := decodeJSON(response.Body, &view); err != nil {
		return V2SelectionView{}, fmt.Errorf("decode v2 selection: %w", err)
	}
	view.Available = true
	return view, nil
}

// UpdateV2Selection writes the multidimensional selection for a link.
func (c *Client) UpdateV2Selection(ctx context.Context, id int64, selection V2SelectionUpdate) (V2SelectionView, error) {
	if id < 1 {
		return V2SelectionView{}, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/v2/links/%d/selection", id), selection)
	if err != nil {
		return V2SelectionView{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return V2SelectionView{}, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return V2SelectionView{}, apiError(response)
	}
	var view V2SelectionView
	if err := decodeJSON(response.Body, &view); err != nil {
		return V2SelectionView{}, fmt.Errorf("decode v2 selection: %w", err)
	}
	view.Available = true
	return view, nil
}

// GetV2Catalog loads the multidimensional vocabulary and converts it into the
// shared taxonomy catalog. Production classification uses this so the compiled
// questions cover topics, content functions, carriers and affordances instead
// of the legacy single-choice subset (F04/F05).
func (c *Client) GetV2Catalog(ctx context.Context) (taxonomy.Catalog, error) {
	vocabulary, err := c.GetV2Taxonomy(ctx)
	if err != nil {
		return taxonomy.Catalog{}, err
	}
	convert := func(terms []TaxonomyTerm) []taxonomy.Term {
		out := make([]taxonomy.Term, 0, len(terms))
		for _, term := range terms {
			out = append(out, taxonomy.Term{
				ID: term.ID, Label: term.Label, Description: term.Description,
				Aliases: term.Aliases, Active: term.Active && !term.Deprecated,
				Includes: term.Includes, Excludes: term.Excludes,
			})
		}
		return out
	}
	catalog := taxonomy.Catalog{
		Version: vocabulary.Version, Topics: convert(vocabulary.Topics),
		Forms: convert(vocabulary.Forms), Uses: convert(vocabulary.Uses),
		ContentFunctions: convert(vocabulary.ContentFunctions),
		Carriers:         convert(vocabulary.Carriers),
		Affordances:      convert(vocabulary.Affordances),
	}
	if err := catalog.Validate(); err != nil {
		return taxonomy.Catalog{}, fmt.Errorf("v2 taxonomy is not a usable catalog: %w", err)
	}
	return catalog, nil
}

// V2Taxonomy is the multidimensional vocabulary. Dimensions not present in an
// older backend are returned as empty slices.
type V2Taxonomy struct {
	Version           string         `json:"version"`
	DefinitionVersion int            `json:"definition_version"`
	Topics            []TaxonomyTerm `json:"topics"`
	Forms             []TaxonomyTerm `json:"forms"`
	Uses              []TaxonomyTerm `json:"uses"`
	ContentFunctions  []TaxonomyTerm `json:"content_functions"`
	Carriers          []TaxonomyTerm `json:"carriers"`
	Affordances       []TaxonomyTerm `json:"affordances"`
}

// TaxonomyTerm is one vocabulary entry in the v2 shape.
type TaxonomyTerm struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Active      bool     `json:"active"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description,omitempty"`
	Includes    []string `json:"includes,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	// DisplayOverridden marks a label that was changed by an approved
	// display-only proposal. It is display metadata: it must never influence the
	// semantic question or the spec hash (R2-10).
	DisplayOverridden bool `json:"display_overridden,omitempty"`
}

// GetV2Taxonomy loads the multidimensional vocabulary.
func (c *Client) GetV2Taxonomy(ctx context.Context) (V2Taxonomy, error) {
	response, err := c.do(ctx, http.MethodGet, "/api/v2/taxonomy", nil)
	if err != nil {
		return V2Taxonomy{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return V2Taxonomy{}, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return V2Taxonomy{}, apiError(response)
	}
	var vocabulary V2Taxonomy
	if err := decodeJSON(response.Body, &vocabulary); err != nil {
		return V2Taxonomy{}, fmt.Errorf("decode v2 taxonomy: %w", err)
	}
	return vocabulary, nil
}

// V2Override is a field-level human action. It is the transport for the
// explicit accept/reject/set_empty/reset controls, and carries the operation
// identity and expected revision needed for idempotency and CAS.
type V2Override struct {
	Field            string `json:"field"`
	Term             string `json:"term,omitempty"`
	Action           string `json:"action"`
	OperationKey     string `json:"operation_key"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

// ApplyV2Override records a field-level human decision.
func (c *Client) ApplyV2Override(ctx context.Context, id int64, override V2Override) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v2/links/%d/overrides", id), override)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode override result: %w", err)
	}
	return payload, nil
}

// GetEvidence reads the latest immutable evidence snapshot for a link. The
// caller renders only the stored blocks, so a missing reference is visibly
// unavailable rather than replaced by a generated explanation.
func (c *Client) GetEvidence(ctx context.Context, id int64) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/evidence", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode evidence snapshot: %w", err)
	}
	return payload, nil
}

// GetClassificationStatus reads the queue state of a link's classification job
// so the UI can distinguish pending/processing/failed/exhausted from an empty
// result instead of showing a red failure for a legitimate empty.
func (c *Client) GetClassificationStatus(ctx context.Context, id int64) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/enrichment/classifications/%d", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode classification status: %w", err)
	}
	return payload, nil
}

// GetEntities reads the independent entity state for a link. A state of
// not_run/failed/stale is different from completed_empty and must stay
// distinguishable in the UI.
func (c *Client) GetEntities(ctx context.Context, id int64) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/entities", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode entity state: %w", err)
	}
	return payload, nil
}

// CorrectEntity records a human correction for one entity candidate. The
// correction is durable and takes precedence over the automatic value.
func (c *Client) CorrectEntity(ctx context.Context, id int64, body map[string]any) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/v2/links/%d/entities", id), body)
}

// SubmitEntityState records one bounded entity lifecycle result.
func (c *Client) SubmitEntityState(ctx context.Context, id int64, body map[string]any) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/v2/links/%d/entity-state", id), body)
}

// EvidenceRequestAck distinguishes a new durable intent from a replay. Neither
// grants permission to fetch: execution requires a separate owned claim.
type EvidenceRequestAck struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Replayed bool   `json:"replayed"`
}

// EvidenceBudget is the immutable fetch bound recorded with the request.
type EvidenceBudget struct {
	MaxBytes  int64 `json:"max_bytes"`
	TimeoutMS int64 `json:"timeout_ms"`
}

// EvidenceReceipt confirms one durable application, including honest no-ops.
type EvidenceReceipt struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	Changed         bool   `json:"changed"`
	Requeued        bool   `json:"requeued"`
	ContentRevision int64  `json:"content_revision,omitempty"`
	ContentHash     string `json:"content_hash,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// EvidenceExecution exposes ownership without disclosing another owner token.
type EvidenceExecution struct {
	ID                 string           `json:"id"`
	LinkID             int64            `json:"link_id"`
	Status             string           `json:"status"`
	Scope              string           `json:"scope"`
	Budget             EvidenceBudget   `json:"budget"`
	ContentRevision    int64            `json:"content_revision"`
	EvidenceSnapshotID int64            `json:"evidence_snapshot_id"`
	SourceHash         string           `json:"source_hash"`
	TargetGeneration   int64            `json:"target_generation"`
	URL                string           `json:"url"`
	Attempts           int              `json:"attempts"`
	Owned              bool             `json:"owned"`
	OwnerToken         *string          `json:"owner_token"`
	LeaseUntil         *string          `json:"lease_until"`
	CheckpointHash     *string          `json:"checkpoint_hash"`
	Receipt            *EvidenceReceipt `json:"receipt"`
}

func (c *Client) evidenceJSON(ctx context.Context, method, path string, body, result any) error {
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	if err := decodeJSON(response.Body, result); err != nil {
		return fmt.Errorf("decode evidence execution: %w", err)
	}
	return nil
}

// CreateEvidenceRequest records or confirms intent; its acknowledgement grants no ownership.
func (c *Client) CreateEvidenceRequest(ctx context.Context, id int64, body map[string]any) (EvidenceRequestAck, error) {
	var result EvidenceRequestAck
	err := c.evidenceJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v2/links/%d/evidence-requests", id), body, &result)
	return result, err
}

// RecoverableEvidenceRequests lists a bounded set of owned-protocol work for a new poll.
func (c *Client) RecoverableEvidenceRequests(ctx context.Context, limit int) ([]EvidenceExecution, error) {
	var result struct {
		Requests []EvidenceExecution `json:"requests"`
	}
	err := c.evidenceJSON(ctx, http.MethodGet, fmt.Sprintf("/api/v2/evidence-requests/recoverable?limit=%d", limit), nil, &result)
	return result.Requests, err
}

// ClaimEvidenceRequest atomically reserves a finite fetch attempt for one owner.
func (c *Client) ClaimEvidenceRequest(ctx context.Context, id, owner string) (EvidenceExecution, error) {
	var result EvidenceExecution
	err := c.evidenceJSON(ctx, http.MethodPost, "/api/v2/evidence-requests/"+url.PathEscape(id)+"/claim", map[string]any{"owner_token": owner}, &result)
	return result, err
}

// CheckpointEvidenceRequest durably saves the outcome before any source or queue change.
func (c *Client) CheckpointEvidenceRequest(ctx context.Context, id, owner string, outcome any) (string, error) {
	var result struct {
		ID             string `json:"id"`
		CheckpointHash string `json:"checkpoint_hash"`
		Replayed       bool   `json:"replayed"`
	}
	err := c.evidenceJSON(ctx, http.MethodPost, "/api/v2/evidence-requests/"+url.PathEscape(id)+"/checkpoint", map[string]any{"owner_token": owner, "outcome": outcome}, &result)
	return result.CheckpointHash, err
}

// FinalizeEvidenceRequest atomically applies a checkpoint and confirms its exact receipt.
func (c *Client) FinalizeEvidenceRequest(ctx context.Context, id, checkpointHash string) (EvidenceReceipt, error) {
	var result EvidenceReceipt
	err := c.evidenceJSON(ctx, http.MethodPost, "/api/v2/evidence-requests/"+url.PathEscape(id)+"/finalize", map[string]any{"checkpoint_hash": checkpointHash}, &result)
	return result, err
}

// DecideEvidenceRequest is the compatibility metadata-only endpoint. Owned
// executions cannot bypass checkpoint/finalize through this legacy method.
func (c *Client) DecideEvidenceRequest(ctx context.Context, requestID string, body map[string]any) error {
	return c.stageWrite(ctx, "/api/v2/evidence-requests/"+url.PathEscape(requestID), body)
}

// GetV2Effective reads the resolved effective view for a link.
func (c *Client) GetV2Effective(ctx context.Context, id int64) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/effective", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode effective view: %w", err)
	}
	return payload, nil
}
