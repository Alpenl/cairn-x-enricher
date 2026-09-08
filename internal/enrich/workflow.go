package enrich

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/compose"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Workflow is the compiled Eino generation and validation pipeline.
type Workflow struct {
	runnable compose.Runnable[Input, Result]
}

// NewWorkflow compiles the Eino generation and validation chain.
func NewWorkflow(ctx context.Context, generator Generator, catalog taxonomy.Catalog) (*Workflow, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	chain := compose.NewChain[Input, Result]()
	chain.AppendLambda(compose.InvokableLambda(generator.Generate))
	chain.AppendLambda(compose.InvokableLambda(func(ctx context.Context, candidate Candidate) (Result, error) {
		result, err := validateCandidate(ctx, candidate)
		if err != nil {
			return Result{}, err
		}
		result.Classification = catalog.Normalize(result.Classification)
		return result, nil
	}))
	runnable, err := chain.Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile Eino enrichment workflow: %w", err)
	}
	return &Workflow{runnable: runnable}, nil
}

// Enrich invokes the compiled Eino workflow for one leased bookmark.
func (w *Workflow) Enrich(ctx context.Context, input Input) (Result, error) {
	result, err := w.runnable.Invoke(ctx, input)
	if err != nil {
		return Result{}, fmt.Errorf("run Eino enrichment workflow: %w", err)
	}
	return result, nil
}
