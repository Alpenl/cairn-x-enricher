package enrich

import (
	"context"
	"testing"
)

// BenchmarkWorkflow measures orchestration and validation, excluding provider
// latency. Run the same fixture before and after changing the workflow.
func BenchmarkWorkflow(b *testing.B) {
	ctx := context.Background()
	generator := generatorFunc(func(_ context.Context, input Input) (Candidate, error) {
		return Candidate{Input: input, SearchVerified: true, Result: Result{
			AITitle: "用于工作流性能测量的中文标题", OriginalLanguage: "en",
			OriginalText:   "A fixed source for the orchestration benchmark.",
			TranslatedText: "用于编排性能测试的固定原文。", Summary: "固定摘要", Model: "fixture",
			RelatedLinks: []string{"https://example.com/article"},
			ImageURLs:    []string{"https://pbs.twimg.com/media/fixture?format=jpg"},
		}}, nil
	})
	w, err := NewWorkflow(generator, testTaxonomy())
	if err != nil {
		b.Fatal(err)
	}
	input := Input{ID: 1, URL: "https://x.com/example/status/1"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := w.Enrich(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}
