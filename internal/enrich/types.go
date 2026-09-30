package enrich

import (
	"context"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Input identifies one leased X bookmark to enrich.
type Input struct {
	ID              int64
	URL             string
	Note            string
	Attempt         int
	LeaseToken      string
	ContentRevision int64
	MinRemainingMS  int64
	Canary          bool
	SourceText      string
	RelatedLinks    []string
}

// PaidAttemptLedger is mandatory for production xAI calls. A reservation
// grants one network send; losing its response never grants another send.
type PaidAttemptLedger interface {
	ReserveProviderAttempt(context.Context, ProviderAttempt) (bool, error)
	SettleProviderAttempt(context.Context, ProviderSettlement) error
	AuthorizeProviderFallback(context.Context, string) error
}

// ProviderAttempt identifies one paid network send before it reaches xAI.
type ProviderAttempt struct {
	OperationKey    string `json:"operation_key"`
	RequestHash     string `json:"request_hash"`
	Model           string `json:"model"`
	Stage           string `json:"stage"`
	Variant         string `json:"variant"`
	AttemptNumber   int    `json:"attempt_number"`
	LinkID          int64  `json:"link_id,omitempty"`
	LeaseToken      string `json:"lease_token,omitempty"`
	ContentRevision int64  `json:"content_revision,omitempty"`
	MinRemainingMS  int64  `json:"min_remaining_ms,omitempty"`
}

// ProviderSettlement records only metadata that the provider actually returned.
type ProviderSettlement struct {
	OperationKey string  `json:"operation_key"`
	HTTPStatus   int     `json:"http_status"`
	ResponseID   *string `json:"response_id"`
	InputTokens  *int64  `json:"input_tokens"`
	OutputTokens *int64  `json:"output_tokens"`
	TotalTokens  *int64  `json:"total_tokens"`
	XSearchCalls *int64  `json:"x_search_calls"`
	CostUSDTicks *int64  `json:"cost_usd_ticks"`
}

// Result is the validated content persisted for a bookmark.
type Result struct {
	AITitle          string
	OriginalLanguage string
	OriginalText     string
	TranslatedText   string
	Summary          string
	RelatedLinks     []string
	ImageURLs        []string
	Model            string
	Classification   taxonomy.Classification
}

// Candidate combines model output with protocol-level search evidence.
type Candidate struct {
	Input          Input
	Result         Result
	SearchVerified bool
}

// Generator obtains one untrusted candidate from a model provider.
type Generator interface {
	Generate(context.Context, Input) (Candidate, error)
}

// Enricher produces a fully validated bookmark result.
type Enricher interface {
	Enrich(context.Context, Input) (Result, error)
}
