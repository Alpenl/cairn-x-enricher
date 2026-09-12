package ablation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture mirrors the recorded regression artifact.
type fixture struct {
	Model   string   `json:"model"`
	Records []Record `json:"records"`
}

// loadFixture reads the recorded outcomes captured from a real experiment run.
// The fixture contains raw model output, so these tests replay it offline: they
// guard the scorer's conclusions without paying for new model calls in CI.
func loadFixture(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join("..", "testdata", "regression-fixture.json")
	//nolint:gosec // fixed test path inside the repository
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(f.Records) == 0 {
		t.Fatal("fixture has no records")
	}
	return f
}

// TestRegressionFixtureIsReproducible re-scores every recorded outcome and
// requires the stored score to match exactly. Scores are a pure function of
// (outcome, sample), so any drift means the scorer or corpus changed in a way
// that would silently alter a published conclusion.
func TestRegressionFixtureIsReproducible(t *testing.T) {
	f := loadFixture(t)
	byID := map[int64]Sample{}
	for _, s := range Samples() {
		byID[s.ID] = s
	}

	for _, rec := range f.Records {
		sample, ok := byID[rec.Sample]
		if !ok {
			t.Errorf("%s sample %d is not in the current corpus", rec.Variant, rec.Sample)
			continue
		}
		got := Judge(rec.Outcome, sample)
		if got.Accepted != rec.Score.Accepted {
			t.Errorf("%s sample %d: Accepted = %v, recorded %v",
				rec.Variant, rec.Sample, got.Accepted, rec.Score.Accepted)
		}
		if got.Grounded != rec.Score.Grounded {
			t.Errorf("%s sample %d: Grounded = %v, recorded %v",
				rec.Variant, rec.Sample, got.Grounded, rec.Score.Grounded)
		}
		if diff := got.QualityScore - rec.Score.QualityScore; diff > 0.001 || diff < -0.001 {
			t.Errorf("%s sample %d: QualityScore = %.4f, recorded %.4f",
				rec.Variant, rec.Sample, got.QualityScore, rec.Score.QualityScore)
		}
		if diff := got.OriginalFidelity - rec.Score.OriginalFidelity; diff > 0.001 || diff < -0.001 {
			t.Errorf("%s sample %d: OriginalFidelity = %.4f, recorded %.4f",
				rec.Variant, rec.Sample, got.OriginalFidelity, rec.Score.OriginalFidelity)
		}
	}
}

// TestRegressionLoadBearingAblationsStayAtZero pins the experiment's two
// headline conclusions. If a future change makes an ungrounded or unparseable
// variant score above zero, the finding "these designs are load-bearing" no
// longer holds and the report must be revisited.
func TestRegressionLoadBearingAblationsStayAtZero(t *testing.T) {
	f := loadFixture(t)
	sums := Summarize(f.Records)

	byName := map[string]VariantSummary{}
	for _, s := range sums {
		byName[s.Variant] = s
	}

	banned := map[string]string{
		"no_x_search":      "removing x_search must not look viable; the model fabricates instead of retrieving",
		"no_strict_schema": "removing the strict schema must not look viable; output becomes unparseable",
	}
	for name, reason := range banned {
		s, ok := byName[name]
		if !ok {
			continue
		}
		if s.MeanQuality > 0.10 {
			t.Errorf("%s mean quality = %.3f, want <= 0.10: %s", name, s.MeanQuality, reason)
		}
	}
}

// TestRegressionFullRemainsStrong pins the baseline. FULL is the production
// configuration, so a collapse here means either the corpus or the scorer
// regressed rather than a genuine finding.
func TestRegressionFullRemainsStrong(t *testing.T) {
	f := loadFixture(t)
	sums := Summarize(f.Records)
	for _, s := range sums {
		if s.Variant != "FULL" {
			continue
		}
		if s.AcceptRate < 1.0 {
			t.Errorf("FULL accept rate = %.2f, want 1.00", s.AcceptRate)
		}
		if s.MeanQuality < 0.95 {
			t.Errorf("FULL mean quality = %.3f, want >= 0.95", s.MeanQuality)
		}
		if s.MeanFidelity < 0.90 {
			t.Errorf("FULL mean fidelity = %.3f, want >= 0.90", s.MeanFidelity)
		}
		return
	}
	t.Fatal("fixture contains no FULL variant; the baseline cannot be checked")
}

// TestRegressionCorpusHasNoPlaceholderURLs prevents the specific mistake that
// invalidated the first run: a sample URL whose author does not match the ID.
// It cannot reach the network, so it asserts the structural invariant that
// every sample is a well-formed x.com status URL and carries reference text to
// score groundedness against.
func TestRegressionCorpusHasNoPlaceholderURLs(t *testing.T) {
	seen := map[int64]bool{}
	for _, s := range Samples() {
		if seen[s.ID] {
			t.Errorf("duplicate sample ID %d", s.ID)
		}
		seen[s.ID] = true
		if s.URL == "" {
			t.Errorf("sample %d has no URL", s.ID)
		}
		if s.ReferenceText == "" {
			t.Errorf("sample %d has no ReferenceText, so groundedness cannot be judged", s.ID)
		}
		key, ok := canonicalOK(s.URL)
		if !ok {
			t.Errorf("sample %d URL %q is not an absolute HTTP(S) URL", s.ID, s.URL)
		}
		if key == "" {
			t.Errorf("sample %d URL %q did not canonicalize", s.ID, s.URL)
		}
		if !hasStatusPath(s.URL) {
			t.Errorf("sample %d URL %q does not look like an x.com status permalink", s.ID, s.URL)
		}
	}
}

// hasStatusPath reports whether a URL contains a /status/<id> path segment.
func hasStatusPath(raw string) bool {
	_, rest, found := strings.Cut(raw, "/status/")
	if !found || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
