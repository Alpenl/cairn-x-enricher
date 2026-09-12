package enrich

import (
	"context"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Workflow generates, validates and normalizes one bookmark result.
type Workflow struct {
	generator Generator
	catalog   taxonomy.Catalog
}

// NewWorkflow validates the catalog once, before processing bookmarks.
func NewWorkflow(generator Generator, catalog taxonomy.Catalog) (*Workflow, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return &Workflow{generator: generator, catalog: catalog}, nil
}

// Enrich keeps provider errors intact, including typed causes and context.
func (w *Workflow) Enrich(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	candidate, err := w.generator.Generate(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result, err := validateCandidate(ctx, candidate)
	if err != nil {
		return Result{}, err
	}
	result.Classification = w.catalog.Normalize(result.Classification)
	return result, nil
}
