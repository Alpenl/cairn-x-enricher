package enrich

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/compose"
)

// Workflow is the compiled Eino generation and validation pipeline.
type Workflow struct {
	runnable compose.Runnable[Input, Result]
}

// NewWorkflow compiles the Eino generation and validation chain.
func NewWorkflow(ctx context.Context, generator Generator) (*Workflow, error) {
	chain := compose.NewChain[Input, Result]()
	chain.AppendLambda(compose.InvokableLambda(generator.Generate))
	chain.AppendLambda(compose.InvokableLambda(validateCandidate))
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
