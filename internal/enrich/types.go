package enrich

import "context"

// Input identifies one leased X bookmark to enrich.
type Input struct {
	ID      int64
	URL     string
	Note    string
	Attempt int
}

// Result is the validated content persisted for a bookmark.
type Result struct {
	OriginalText string
	Summary      string
	RelatedLinks []string
	Model        string
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
