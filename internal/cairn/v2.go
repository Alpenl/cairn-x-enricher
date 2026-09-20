package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// V2SelectionView is the response shape for a selection read. `available` is
// false when the Worker does not support v2; the caller must then fall back to
// the v1 projection rather than treating the selection as empty.
type V2SelectionView struct {
	ID              int64       `json:"id"`
	Selection       V2Selection `json:"selection"`
	TaxonomyVersion string      `json:"taxonomy_version,omitempty"`
	V1Only          bool        `json:"v1_only,omitempty"`
	Revision        int64       `json:"revision"`
	Available       bool        `json:"-"`
	V1Projection    V2Selection `json:"v1_projection,omitempty"`
}

// ErrV2Unsupported reports that the Worker does not implement the v2 API. It is
// distinct from an empty selection so the UI can degrade to read-only instead
// of showing an empty record.
var ErrV2Unsupported = errors.New("backend does not support the v2 selection API")

// GetV2Selection reads the multidimensional selection for a link.
func (c *Client) GetV2Selection(ctx context.Context, id int64) (V2SelectionView, error) {
	if id < 1 {
		return V2SelectionView{}, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/selection", id), nil)
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
func (c *Client) UpdateV2Selection(ctx context.Context, id int64, selection V2Selection) (V2SelectionView, error) {
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
