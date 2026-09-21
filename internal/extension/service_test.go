package extension

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

type fakeJudge struct {
	answers map[string]classify.RawAnswer
	err     error
	calls   int
	lastQ   map[string]classify.ProviderQuestion
}

func (f *fakeJudge) Judge(_ context.Context, _ any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	f.calls++
	f.lastQ = questions
	if f.err != nil {
		return nil, f.err
	}
	return f.answers, nil
}

func noul(value float64) classify.RawAnswer {
	return classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &value}}
}

func score(value float64) classify.RawAnswer {
	return classify.RawAnswer{Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: value}}
}

// TestEntitiesDisabledIsNotRun proves a disabled extension never calls the
// model and never claims a completed empty result.
func TestEntitiesDisabledIsNotRun(t *testing.T) {
	judge := &fakeJudge{}
	service := NewService(DefaultFlags(), DefaultBudget(), judge)
	result := service.Entities(context.Background(), []Block{{ID: "primary-1", Text: "Acme builds Widgets."}}, nil)
	if result.State != EntityNotRun || judge.calls != 0 {
		t.Fatalf("disabled entities must not run: %+v calls=%d", result, judge.calls)
	}
}

func TestEntitiesValidatesCandidatesAndKeepsDistinctNames(t *testing.T) {
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{
		"entity_0": noul(0.95), "entity_1": noul(0.9),
	}}
	flags := DefaultFlags()
	flags.Entities = true
	service := NewService(flags, DefaultBudget(), judge)
	result := service.Entities(context.Background(),
		[]Block{{ID: "primary-1", Text: "Acme builds Widgets. Acme ships."}}, nil)
	if result.State != EntityCompletedNonempty {
		t.Fatalf("state = %s", result.State)
	}
	// Acme appears twice but is one entity; Widgets is a different name and is
	// never merged into it.
	if len(result.Entities) != 2 || result.Entities[0] != "Acme" || result.Entities[1] != "Widgets" {
		t.Fatalf("entities = %v", result.Entities)
	}
	if len(judge.lastQ) != 2 {
		t.Fatalf("expected one question per candidate, got %d", len(judge.lastQ))
	}
}

func TestEntitiesCompletedEmptyIsASuccess(t *testing.T) {
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{"entity_0": noul(0.1)}}
	flags := DefaultFlags()
	flags.Entities = true
	service := NewService(flags, DefaultBudget(), judge)
	result := service.Entities(context.Background(), []Block{{ID: "primary-1", Text: "Acme"}}, nil)
	if result.State != EntityCompletedEmpty || len(result.Entities) != 0 {
		t.Fatalf("a legitimate empty must be a completed empty: %+v", result)
	}
}

func TestEntitiesBudgetExhaustionAndProviderFailureAreExplicit(t *testing.T) {
	flags := DefaultFlags()
	flags.Entities = true
	tiny := DefaultBudget()
	tiny.MaxTokens = 10
	exhausted := NewService(flags, tiny, &fakeJudge{answers: map[string]classify.RawAnswer{}})
	result := exhausted.Entities(context.Background(), []Block{{ID: "primary-1", Text: "Acme Widgets"}}, nil)
	if result.State != EntityFailed || !strings.Contains(result.Reason, "budget") {
		t.Fatalf("budget exhaustion must be explicit: %+v", result)
	}
	failing := NewService(flags, DefaultBudget(), &fakeJudge{err: errors.New("provider down")})
	result = failing.Entities(context.Background(), []Block{{ID: "primary-1", Text: "Acme Widgets"}}, nil)
	if result.State != EntityFailed || !strings.Contains(result.Reason, "provider down") {
		t.Fatalf("provider failure must be explicit: %+v", result)
	}
}

// TestEntitiesRejectsAnAnswerOutsideTheCandidateSet proves a model answer can
// never introduce an entity the extractor did not find.
func TestEntitiesRejectsAnAnswerOutsideTheCandidateSet(t *testing.T) {
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{
		"entity_0": noul(0.95), "entity_9": noul(0.99),
	}}
	flags := DefaultFlags()
	flags.Entities = true
	service := NewService(flags, DefaultBudget(), judge)
	result := service.Entities(context.Background(), []Block{{ID: "primary-1", Text: "Acme"}}, nil)
	if result.State != EntityFailed || !strings.Contains(result.Reason, "unknown candidate") {
		t.Fatalf("an out-of-set verdict must fail the run: %+v", result)
	}
}

func TestDetectGapIsConservative(t *testing.T) {
	if gap := DetectGap([]Block{{ID: "primary-1", Text: "body"}}, nil, false); gap != GapNone {
		t.Fatalf("no stored link and no truncation is not a gap: %s", gap)
	}
	if gap := DetectGap([]Block{{ID: "primary-1", Text: "body"}}, []string{"https://example.com/a"}, false); gap != GapExternalLink {
		t.Fatalf("a stored external link is a gap: %s", gap)
	}
	if gap := DetectGap([]Block{{ID: "external-1", Text: "fetched"}}, []string{"https://example.com/a"}, false); gap != GapNone {
		t.Fatalf("an already fetched external block is not a gap: %s", gap)
	}
	if gap := DetectGap(nil, nil, true); gap != GapTruncation {
		t.Fatalf("truncation is a gap: %s", gap)
	}
}

func TestRerankFallbacksKeepTheOriginalOrder(t *testing.T) {
	candidates := []Candidate{
		{ID: "1", Rank: 0, Allowed: true}, {ID: "2", Rank: 1, Allowed: true},
	}
	flags := DefaultFlags()
	flags.Rerank = true
	service := NewService(flags, DefaultBudget(), &fakeJudge{err: errors.New("down")})
	result := service.RerankCandidates(context.Background(), "query", candidates)
	if result.Applied || result.Candidates[0].ID != "1" || result.Candidates[1].ID != "2" {
		t.Fatalf("a failed rerank must keep the original order: %+v", result)
	}
	// A disabled flag never calls the model.
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{"rerank_0": score(3), "rerank_1": score(0)}}
	disabled := NewService(DefaultFlags(), DefaultBudget(), judge)
	result = disabled.RerankCandidates(context.Background(), "query", candidates)
	if result.Applied || judge.calls != 0 {
		t.Fatalf("disabled rerank must not call the model: %+v calls=%d", result, judge.calls)
	}
}

func TestRerankOrdersBySharedRubricScore(t *testing.T) {
	candidates := []Candidate{
		{ID: "1", Rank: 0, Allowed: true}, {ID: "2", Rank: 1, Allowed: true}, {ID: "3", Rank: 2, Allowed: false},
	}
	judge := &fakeJudge{answers: map[string]classify.RawAnswer{"rerank_0": score(0), "rerank_1": score(3)}}
	flags := DefaultFlags()
	flags.Rerank = true
	service := NewService(flags, DefaultBudget(), judge)
	result := service.RerankCandidates(context.Background(), "query", candidates)
	if !result.Applied || len(result.Candidates) != 2 || result.Candidates[0].ID != "2" {
		t.Fatalf("rerank did not order by score: %+v", result)
	}
	// A denied candidate is never returned.
	for _, candidate := range result.Candidates {
		if candidate.ID == "3" {
			t.Fatal("a denied candidate leaked into the rerank result")
		}
	}
}

// TestRequestEvidenceRespectsPolicyAndDisabledFlag covers the blocked path
// without any network access: an empty allowlist denies everything.
func TestRequestEvidenceRespectsPolicyAndDisabledFlag(t *testing.T) {
	service := NewService(DefaultFlags(), DefaultBudget(), &fakeJudge{})
	outcome := service.RequestEvidence(context.Background(), nil, DefaultFetchPolicy(nil), "https://example.com/a")
	if outcome.State != "blocked" || !strings.Contains(outcome.Reason, "disabled") {
		t.Fatalf("a disabled evidence extension must be blocked: %+v", outcome)
	}
	flags := DefaultFlags()
	flags.Evidence = true
	enabled := NewService(flags, DefaultBudget(), &fakeJudge{})
	outcome = enabled.RequestEvidence(context.Background(), nil, DefaultFetchPolicy(nil), "https://example.com/a")
	if outcome.State != "blocked" || !strings.Contains(outcome.Reason, "allowlist") {
		t.Fatalf("an empty allowlist must deny the fetch: %+v", outcome)
	}
	// A non-allowlisted host is blocked before any dial.
	policy := DefaultFetchPolicy([]string{"allowed.example"})
	outcome = enabled.RequestEvidence(context.Background(), nil, policy, "https://other.example/a")
	if outcome.State != "blocked" {
		t.Fatalf("a non-allowlisted host must be blocked: %+v", outcome)
	}
}
