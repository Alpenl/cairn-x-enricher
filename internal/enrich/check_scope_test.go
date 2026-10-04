package enrich

import (
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestReadingCheckScopeIsStableAndConfigurationBound(t *testing.T) {
	makeScope := func(base, key, model string, tokens int) string {
		return NewResponsesClient(base, key, model, tokens, "ignored", nil, taxonomy.Catalog{}).ReadingCheckScope()
	}
	original := makeScope("https://example.test/v1", "secret", "model", 1000)
	if len(original) != 64 || original != makeScope("https://example.test/v1", "secret", "model", 1000) {
		t.Fatal("unstable scope")
	}
	for _, changed := range []string{makeScope("https://other.test/v1", "secret", "model", 1000), makeScope("https://example.test/v1", "new-secret", "model", 1000), makeScope("https://example.test/v1", "secret", "new-model", 1000), makeScope("https://example.test/v1", "secret", "model", 2000)} {
		if changed == original {
			t.Fatal("configuration did not invalidate scope")
		}
	}
}
