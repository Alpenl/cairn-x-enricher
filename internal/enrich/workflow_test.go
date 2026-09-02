package enrich

import (
	"context"
	"errors"
	"testing"
)

type generatorFunc func(context.Context, Input) (Candidate, error)

func (f generatorFunc) Generate(ctx context.Context, input Input) (Candidate, error) {
	return f(ctx, input)
}

func TestWorkflowValidatesAndNormalizesModelResult(t *testing.T) {
	input := Input{ID: 5, URL: "https://x.com/user/status/5", Attempt: 1}
	generator := generatorFunc(func(_ context.Context, got Input) (Candidate, error) {
		return Candidate{
			Input: got,
			Result: Result{
				OriginalText: "  source text  ",
				Summary:      "  short summary  ",
				RelatedLinks: []string{
					"https://example.com/article#one",
					"https://EXAMPLE.com/article#two",
					input.URL,
				},
				Model: " grok-4.6 ",
			},
			SearchVerified: true,
		}, nil
	})

	workflow, err := NewWorkflow(context.Background(), generator)
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	result, err := workflow.Enrich(context.Background(), input)
	if err != nil {
		t.Fatalf("Enrich() error = %v", err)
	}
	if result.OriginalText != "source text" || result.Summary != "short summary" || result.Model != "grok-4.6" {
		t.Fatalf("Enrich() = %+v", result)
	}
	if len(result.RelatedLinks) != 1 || result.RelatedLinks[0] != "https://example.com/article#one" {
		t.Fatalf("RelatedLinks = %#v", result.RelatedLinks)
	}
}

func TestWorkflowRejectsUnverifiedSearch(t *testing.T) {
	generator := generatorFunc(func(_ context.Context, input Input) (Candidate, error) {
		return Candidate{
			Input:  input,
			Result: Result{OriginalText: "text", Summary: "summary", Model: "model"},
		}, nil
	})
	workflow, err := NewWorkflow(context.Background(), generator)
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	if _, err := workflow.Enrich(context.Background(), Input{URL: "https://x.com/a/status/1"}); err == nil {
		t.Fatal("Enrich() error = nil, want missing search evidence error")
	}
}

func TestWorkflowPropagatesGeneratorFailure(t *testing.T) {
	want := errors.New("upstream unavailable")
	generator := generatorFunc(func(context.Context, Input) (Candidate, error) {
		return Candidate{}, want
	})
	workflow, err := NewWorkflow(context.Background(), generator)
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	if _, err := workflow.Enrich(context.Background(), Input{}); !errors.Is(err, want) {
		t.Fatalf("Enrich() error = %v, want wrapped %v", err, want)
	}
}
