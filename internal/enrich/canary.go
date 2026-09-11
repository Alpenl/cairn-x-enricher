package enrich

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// canarySourceText is a tiny, deterministic sample used only to prove the
// endpoint still honours the strict JSON Schema contract. It never reaches a
// bookmark and is not stored anywhere.
const canarySourceText = "Canary check: the model endpoint must return one structured enrichment object."

// Canary verifies at startup that the model endpoint still satisfies the
// contract this service depends on. Without it, a provider-side change to
// strict schema support or field naming would fail every job silently until
// an operator noticed the exhausted queue.
//
// It uses the trusted-source path so it costs one short request and never
// invokes x_search.
func (c *ResponsesClient) Canary(ctx context.Context) error {
	candidate, err := c.generateFromSource(ctx, Input{
		ID:         0,
		URL:        "https://x.com/canary/status/0",
		Attempt:    1,
		SourceText: canarySourceText,
	})
	if err != nil {
		return fmt.Errorf("model contract canary request failed: %w", err)
	}
	if err := c.verifyCanaryShape(candidate); err != nil {
		return fmt.Errorf("model contract canary returned an unusable shape: %w", err)
	}
	return nil
}

// verifyCanaryShape checks the parts of the contract a provider can silently
// change: the strict schema must be honoured, so unknown fields are rejected
// during decode, and the required keys must be present and non-empty.
func (c *ResponsesClient) verifyCanaryShape(candidate Candidate) error {
	missing := make([]string, 0, 4)
	for name, value := range map[string]string{
		"ai_title":          candidate.Result.AITitle,
		"original_language": candidate.Result.OriginalLanguage,
		"translated_text":   candidate.Result.TranslatedText,
		"summary":           candidate.Result.Summary,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		// Map iteration order is random, so sort for a stable message that
		// does not churn logs across restarts.
		slices.Sort(missing)
		return fmt.Errorf("strict schema keys missing or empty: %s", strings.Join(missing, ", "))
	}
	if !containsHan(candidate.Result.AITitle) {
		return errors.New("ai_title did not contain Chinese, which the schema and prompt require")
	}
	return nil
}
