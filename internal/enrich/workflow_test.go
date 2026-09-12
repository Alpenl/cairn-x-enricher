package enrich

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
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
				AITitle:          "  人工智能生成的测试中文标题  ",
				OriginalLanguage: " en ",
				OriginalText:     "  source text  ",
				TranslatedText:   "  简体中文译文  ",
				Summary:          "  short summary  ",
				RelatedLinks: []string{
					"https://example.com/article#one",
					"https://EXAMPLE.com/article#two",
					input.URL,
				},
				ImageURLs: []string{
					" https://pbs.twimg.com/media/abc?format=jpg&name=large#ignored ",
					"https://pbs.twimg.com/media/abc?format=jpg&name=large",
				},
				Model: " grok-4.6 ",
				Classification: taxonomy.Classification{Selection: taxonomy.Selection{
					Topics: []string{"ＡＩ", "invented"}, Form: "工具", Use: "待试",
				}},
			},
			SearchVerified: true,
		}, nil
	})

	workflow, err := NewWorkflow(generator, testTaxonomy())
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	result, err := workflow.Enrich(context.Background(), input)
	if err != nil {
		t.Fatalf("Enrich() error = %v", err)
	}
	if result.AITitle != "人工智能生成的测试中文标题" || result.OriginalLanguage != "en" || result.OriginalText != "source text" || result.TranslatedText != "简体中文译文" || result.Summary != "short summary" || result.Model != "grok-4.6" {
		t.Fatalf("Enrich() = %+v", result)
	}
	if len(result.RelatedLinks) != 1 || result.RelatedLinks[0] != "https://example.com/article#one" {
		t.Fatalf("RelatedLinks = %#v", result.RelatedLinks)
	}
	if len(result.ImageURLs) != 1 || result.ImageURLs[0] != "https://pbs.twimg.com/media/abc?format=jpg&name=large" {
		t.Fatalf("ImageURLs = %#v", result.ImageURLs)
	}
	if len(result.Classification.Topics) != 1 || result.Classification.Topics[0] != "llm" || !result.Classification.Uncertainty || result.Classification.TaxonomyVersion != "test-v1" || len(result.Classification.DiscardedTags) != 1 {
		t.Fatalf("unvalidated classification reached workflow output: %+v", result.Classification)
	}
}

func TestWorkflowRejectsUnverifiedSearch(t *testing.T) {
	generator := generatorFunc(func(_ context.Context, input Input) (Candidate, error) {
		return Candidate{
			Input: input,
			Result: Result{
				AITitle: "人工智能生成的测试中文标题", OriginalLanguage: "en",
				OriginalText: "text", TranslatedText: "译文", Summary: "summary", Model: "model",
			},
		}, nil
	})
	workflow, err := NewWorkflow(generator, testTaxonomy())
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	if _, err := workflow.Enrich(context.Background(), Input{URL: "https://x.com/a/status/1"}); err == nil {
		t.Fatal("Enrich() error = nil, want missing search evidence error")
	}
}

func TestWorkflowRejectsUnsafeImageURLAndNonChineseTitle(t *testing.T) {
	for _, result := range []Result{
		{
			AITitle: "This title is not Chinese", OriginalLanguage: "en", OriginalText: "text",
			TranslatedText: "译文", Summary: "摘要", Model: "model",
		},
		{
			AITitle: "人工智能生成的测试中文标题", OriginalLanguage: "en", OriginalText: "text",
			TranslatedText: "译文", Summary: "摘要", ImageURLs: []string{"https://example.com/tracker.jpg"}, Model: "model",
		},
	} {
		generator := generatorFunc(func(_ context.Context, input Input) (Candidate, error) {
			return Candidate{Input: input, Result: result, SearchVerified: true}, nil
		})
		workflow, err := NewWorkflow(generator, testTaxonomy())
		if err != nil {
			t.Fatalf("NewWorkflow() error = %v", err)
		}
		if _, err := workflow.Enrich(context.Background(), Input{URL: "https://x.com/a/status/1"}); err == nil {
			t.Fatalf("Enrich() accepted invalid result: %+v", result)
		}
	}
}

func TestWorkflowPropagatesGeneratorFailure(t *testing.T) {
	want := errors.New("upstream unavailable")
	generator := generatorFunc(func(context.Context, Input) (Candidate, error) {
		return Candidate{}, want
	})
	workflow, err := NewWorkflow(generator, testTaxonomy())
	if err != nil {
		t.Fatalf("NewWorkflow() error = %v", err)
	}
	if _, err := workflow.Enrich(context.Background(), Input{}); !errors.Is(err, want) {
		t.Fatalf("Enrich() error = %v, want wrapped %v", err, want)
	}
}

func testTaxonomy() taxonomy.Catalog {
	return taxonomy.Catalog{
		Version: "test-v1",
		Topics:  []taxonomy.Term{{ID: "llm", Label: "LLM", Aliases: []string{"AI", "大模型"}, Active: true}},
		Forms:   []taxonomy.Term{{ID: "tool", Label: "工具", Active: true}},
		Uses:    []taxonomy.Term{{ID: "try", Label: "待试", Active: true}},
	}
}

func TestWorkflowPreservesProviderErrorContext(t *testing.T) {
	cause := &ModelHTTPError{StatusCode: 502}
	want := fmt.Errorf("read search response: %w", cause)
	w, err := NewWorkflow(generatorFunc(func(context.Context, Input) (Candidate, error) {
		return Candidate{}, want
	}), testTaxonomy())
	if err != nil {
		t.Fatal(err)
	}
	_, got := w.Enrich(context.Background(), Input{})
	if !errors.Is(got, want) || got.Error() != want.Error() || !errors.Is(got, cause) {
		t.Fatalf("provider context lost: %v", got)
	}
}

func TestWorkflowStopsAtCancellationBoundaries(t *testing.T) {
	for _, duringGeneration := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		called := false
		w, err := NewWorkflow(generatorFunc(func(context.Context, Input) (Candidate, error) {
			called = true
			cancel()
			return Candidate{}, nil
		}), testTaxonomy())
		if err != nil {
			t.Fatal(err)
		}
		if !duringGeneration {
			cancel()
		}
		_, got := w.Enrich(ctx, Input{})
		cancel()
		if !errors.Is(got, context.Canceled) || called != duringGeneration {
			t.Fatalf("cancellation boundary: called=%v err=%v", called, got)
		}
	}
}
