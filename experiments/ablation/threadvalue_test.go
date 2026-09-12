package ablation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// threadPair mirrors one record in the real-collection thread-value artifact.
type threadPair struct {
	ID         int64  `json:"id"`
	SameText   bool   `json:"same_text"`
	LenDelta   int    `json:"len_delta"`
	PostLen    int    `json:"post_len"`
	ThreadLen  int    `json:"thread_len"`
	TokenDelta int    `json:"token_delta"`
	ThreadErr  string `json:"thread_err"`
	PostErr    string `json:"post_err"`
}

type threadReport struct {
	Model string       `json:"model"`
	Pairs []threadPair `json:"pairs"`
}

func loadThreadReport(t *testing.T) (threadReport, bool) {
	t.Helper()
	path := filepath.Join("..", "results", "thread-value.json")
	//nolint:gosec // fixed path inside the repository
	raw, err := os.ReadFile(path)
	if err != nil {
		return threadReport{}, false
	}
	var r threadReport
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode thread-value.json: %v", err)
	}
	return r, true
}

// TestThreadValueFindingsHold pins the real-collection conclusion: thread
// retrieval does not change the retrieved original text for the large majority
// of bookmarks, so it cannot be justified as a quality feature. The test skips
// when the artifact is absent, because it is produced by a paid live run and is
// not committed to every checkout.
func TestThreadValueFindingsHold(t *testing.T) {
	report, ok := loadThreadReport(t)
	if !ok {
		t.Skip("thread-value.json not present; run experiments/threadvalue to regenerate")
	}
	if len(report.Pairs) < 10 {
		t.Fatalf("only %d pairs recorded; too few to assert a rate", len(report.Pairs))
	}

	var comparable, identical, postLonger int
	for _, p := range report.Pairs {
		// Only pairs where both variants produced text can be compared.
		if p.ThreadErr != "" || p.PostErr != "" || p.ThreadLen == 0 || p.PostLen == 0 {
			continue
		}
		comparable++
		if p.SameText {
			identical++
		}
		if p.LenDelta < 0 {
			postLonger++
		}
	}
	if comparable < 8 {
		t.Fatalf("only %d comparable pairs; measurement is inconclusive", comparable)
	}

	// The published claim: thread context rarely reaches the stored original.
	if rate := float64(identical) / float64(comparable); rate < 0.70 {
		t.Errorf("identical-text rate = %.2f over %d pairs, want >= 0.70; "+
			"the thread-value conclusion no longer holds", rate, comparable)
	}
	// The published claim: thread retrieval never loses text relative to
	// post-only, which is why it can be kept as reliability insurance.
	if postLonger > 0 {
		t.Errorf("post-only produced more text in %d pairs, want 0; "+
			"thread retrieval would then be actively harmful", postLonger)
	}
}

// TestThreadValueFailedMoreOftenWithoutComments checks the reliability claim:
// the thread variant should not fail more often than post-only.
func TestThreadValueFailedMoreOftenWithoutComments(t *testing.T) {
	report, ok := loadThreadReport(t)
	if !ok {
		t.Skip("thread-value.json not present")
	}
	var threadFail, postFail int
	for _, p := range report.Pairs {
		if p.ThreadErr != "" {
			threadFail++
		}
		if p.PostErr != "" {
			postFail++
		}
	}
	if threadFail > postFail {
		t.Errorf("thread variant failed %d times vs post-only %d; "+
			"dropping comment retrieval would improve reliability", threadFail, postFail)
	}
}
