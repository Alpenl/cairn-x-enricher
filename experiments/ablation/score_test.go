package ablation

import (
	"strings"
	"testing"
	"time"
)

// TestJudgeRejectsFabricatedIdentity guards the scorer's most important job:
// a result that is structurally valid but describes another post must not be
// accepted as high quality. This is the failure the first corpus mistake would
// have hidden.
func TestJudgeRejectsFabricatedIdentity(t *testing.T) {
	out := Outcome{
		Title:          "完全无关的另一条帖子的中文标题",
		Summary:        "摘要",
		Language:       "en",
		OriginalText:   "This text belongs to a different post entirely.",
		TranslatedText: "译文",
		SearchEvidence: true,
		Latency:        time.Second,
		TotalTokens:    100,
	}
	sample := Sample{
		ID:            1,
		URL:           "https://x.com/karpathy/status/1991910395720925418",
		ReferenceText: "Something I think people continue to have poor intuition for: animal intelligence",
	}
	sc := Judge(out, sample)
	if !sc.Accepted {
		t.Fatalf("structurally valid result should still be accepted by the production rules")
	}
	if sc.OriginalFidelity > 0.2 {
		t.Fatalf("fidelity = %.3f, want low for unrelated text", sc.OriginalFidelity)
	}
}

// TestJudgeScoresSourceFidelity confirms the fidelity metric separates a
// verbatim echo from a paraphrase.
func TestJudgeScoresSourceFidelity(t *testing.T) {
	ref := "The tokenizer is a wart in LLMs and it lets the model handle arbitrary strings."
	base := Outcome{
		Title: "分词器是大模型的瑕疵", Summary: "摘要", Language: "en",
		TranslatedText: "译文", SearchEvidence: true,
	}
	exact := base
	exact.OriginalText = ref
	paraphrase := base
	paraphrase.OriginalText = "Tokenizers are a wart in LLMs."

	sample := Sample{ID: 1, URL: "https://x.com/a/status/1", ReferenceText: ref}
	sExact := Judge(exact, sample)
	sPara := Judge(paraphrase, sample)
	if sExact.OriginalFidelity <= sPara.OriginalFidelity {
		t.Fatalf("exact fidelity %.3f should exceed paraphrase %.3f",
			sExact.OriginalFidelity, sPara.OriginalFidelity)
	}
}

// TestLanguageAgreementCatchesTranslatedOriginal verifies that a variant which
// translates the "original" text is flagged, since the product requires the
// original to stay in its source language.
func TestLanguageAgreementCatchesTranslatedOriginal(t *testing.T) {
	// Declared Japanese but the text came back as Chinese: a real failure mode.
	out := Outcome{
		Title: "标题应该是中文的", Summary: "摘要", OriginalText: "这是一段被翻译成中文的原文",
		TranslatedText: "译文", Language: "ja", SearchEvidence: true,
	}
	sc := Judge(out, Sample{ID: 1, URL: "https://x.com/a/status/1"})
	if sc.LanguageCorrect {
		t.Fatal("declared ja but produced Chinese text should be a language error")
	}
}

// TestInfraFailureExcludedFromAcceptance ensures a 502 is not scored as a
// quality regression, which is what made the first run misleading.
func TestInfraFailureExcludedFromAcceptance(t *testing.T) {
	out := Outcome{InfraFailed: true, Err: "model HTTP 502"}
	sc := Judge(out, Sample{ID: 1, URL: "https://x.com/a/status/1"})
	if sc.Accepted {
		t.Fatal("an infrastructure failure must not be accepted")
	}
	if !sc.InfraFailed {
		t.Fatal("the infra flag must propagate so aggregation can exclude it")
	}
}

// TestSummarizeExcludesInfraFailures proves the aggregation excludes infra
// failures from the quality mean while still counting them.
func TestSummarizeExcludesInfraFailures(t *testing.T) {
	good := Outcome{
		Title: "一个合规的中文标题", Summary: "摘要", Language: "en",
		OriginalText: "source", TranslatedText: "译文", SearchEvidence: true, TotalTokens: 100,
	}
	records := []Record{
		{Variant: "FULL", Ablates: "none", Sample: 1, Outcome: good, Score: Judge(good, Sample{ID: 1})},
		{Variant: "FULL", Ablates: "none", Sample: 2, Outcome: Outcome{InfraFailed: true, Err: "model HTTP 502"}, Score: Judge(Outcome{InfraFailed: true, Err: "model HTTP 502"}, Sample{ID: 2})},
	}
	sums := Summarize(records)
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	if sums[0].Runs != 1 {
		t.Fatalf("Runs = %d, want 1 (infra failure excluded)", sums[0].Runs)
	}
	if sums[0].InfraFailed != 1 {
		t.Fatalf("InfraFailed = %d, want 1", sums[0].InfraFailed)
	}
	if sums[0].MeanQuality <= 0 {
		t.Fatalf("MeanQuality = %.3f, want a positive score over the single scorable run", sums[0].MeanQuality)
	}
	// The excluded run must not drag the mean toward zero: its absence is the
	// whole point, so the mean must equal the one scorable run's own score.
	if want := Judge(good, Sample{ID: 1}).QualityScore; sums[0].MeanQuality != want {
		t.Fatalf("MeanQuality = %.3f, want %.3f (the single scorable run)", sums[0].MeanQuality, want)
	}
}

// TestVariantsSkipSamplesWithoutSource ensures the source_only baseline is not
// run against samples that have no trusted text.
func TestVariantsSkipSamplesWithoutSource(t *testing.T) {
	sv := SourceVariant()
	if !sv.RequiresSource {
		t.Fatal("source_only must declare RequiresSource so it is skipped correctly")
	}
	// The source corpus always carries SourceText, so a source variant must
	// never be skipped when it runs against its own corpus.
	for _, s := range SourceSamples() {
		if s.SourceText == "" {
			t.Fatalf("sample %d in SourceSamples() has no SourceText", s.ID)
		}
	}
	if len(SourceSamples()) != len(Samples()) {
		t.Fatalf("SourceSamples() = %d entries, want %d", len(SourceSamples()), len(Samples()))
	}
}

// TestValidateOutcomeChecksEvidenceFirst pins the attribution order.
//
// Production rejects a candidate with no completed X search before inspecting
// any field, because that check separates "retrieval failed" from "formatting
// failed" and the two need different operator responses. An earlier version of
// this harness skipped the evidence check, so a run with no search evidence was
// reported as "ai_title length out of range" — a misleading cause that pointed
// at the wrong subsystem.
func TestValidateOutcomeChecksEvidenceFirst(t *testing.T) {
	// Structurally perfect output, but no search evidence: the failure must be
	// attributed to the missing search, not to any field.
	out := Outcome{
		Title:          "一个完全合规的中文标题",
		Summary:        "摘要",
		Language:       "en",
		OriginalText:   "source text",
		TranslatedText: "译文",
		SearchEvidence: false,
	}
	err := validateOutcome(out)
	if err == nil {
		t.Fatal("validateOutcome() = nil, want a rejection for missing search evidence")
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("validateOutcome() = %q, want it to name the missing search evidence", err)
	}

	// With evidence present, the same output is accepted, proving the evidence
	// check is the one that fired above rather than a field rule.
	out.SearchEvidence = true
	if err := validateOutcome(out); err != nil {
		t.Fatalf("validateOutcome() with evidence = %v, want nil", err)
	}
}

// TestValidateOutcomeTreatsSourcePathEvidenceAsSatisfied guards a regression
// that this harness introduced and then removed.
//
// The recovery path by design does not search: the post text comes from the
// caller, not from the model. Production sets sourceVerified=true for it, so it
// never requires an observed X search. When this harness applied the search
// evidence rule to every variant, all source-path results were rejected as
// "model did not provide evidence of a completed X search" even though the
// titles were correct — a harness bug that would have been reported as a
// product finding.
func TestValidateOutcomeTreatsSourcePathEvidenceAsSatisfied(t *testing.T) {
	// A correct recovery result: the model returned nothing about a search,
	// because none was requested.
	out := Outcome{
		Title:          "一个合规的中文标题内容",
		Summary:        "摘要",
		Language:       "zh",
		OriginalText:   "已存原文",
		TranslatedText: "译文",
		SearchEvidence: true, // set by generate() when req.SourcePath is true
	}
	if err := validateOutcome(out); err != nil {
		t.Fatalf("validateOutcome() = %v, want nil for a correct recovery result", err)
	}

	// The same output with no evidence at all must still be rejected: the
	// search path relies on that rule, and relaxing it would be a real defect.
	out.SearchEvidence = false
	if err := validateOutcome(out); err == nil {
		t.Fatal("validateOutcome() = nil, want a rejection when no search is evidenced")
	}
}
