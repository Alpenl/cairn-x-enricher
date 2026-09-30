package extension

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

type cacheJudge struct{ budgetJudge }

func (j *cacheJudge) PreparedJudgeRequest(state any, questions map[string]classify.ProviderQuestion) ([]byte, error) {
	return json.Marshal(map[string]any{"model": "jev-1.13.0", "state": state, "questions": questions})
}

type cacheFixture struct {
	receipt                 RerankReceipt
	claimErr                error
	completeErr             bool
	hideSaved               bool
	claims, completes, gets int
	completion              RerankCompletion
}

func (f *cacheFixture) ClaimRerank(context.Context, RerankClaim) (RerankReceipt, error) {
	f.claims++
	return f.receipt, f.claimErr
}
func (f *cacheFixture) CompleteRerank(_ context.Context, _ string, c RerankCompletion) (RerankReceipt, error) {
	f.completes++
	f.completion = c
	if !f.hideSaved {
		f.receipt.Status = c.Status
		f.receipt.Answers = c.Answers
		f.receipt.Owned = false
	}
	if f.completeErr {
		return RerankReceipt{}, errors.New("lost completion response")
	}
	return f.receipt, nil
}
func (f *cacheFixture) GetRerank(context.Context, string) (RerankReceipt, error) {
	f.gets++
	return f.receipt, nil
}
func cacheCandidates() []Candidate {
	return []Candidate{{ID: "1", Text: "synthetic material", Allowed: true, CacheItem: &CacheItem{ID: 1}}}
}

func TestRerankCacheFailureNeverAuthorizesAnotherModelAttempt(t *testing.T) {
	for _, mode := range []string{"backend_error", "pending", "failed", "missing_version", "lost_response", "unconfirmed", "model_failed", "invalid_hit"} {
		t.Run(mode, func(t *testing.T) {
			judge := &cacheJudge{}
			fixture := &cacheFixture{receipt: RerankReceipt{Key: "synthetic-key", Status: "pending", Owned: true, Answers: map[string]classify.RawAnswer{}}}
			candidates := cacheCandidates()
			switch mode {
			case "backend_error":
				fixture.claimErr = errors.New("unsupported backend")
			case "pending":
				fixture.receipt.Owned = false
			case "failed":
				fixture.receipt.Status = "failed"
				fixture.receipt.Owned = false
			case "missing_version":
				candidates[0].CacheItem = nil
			case "lost_response":
				fixture.completeErr = true
			case "unconfirmed":
				fixture.completeErr = true
				fixture.hideSaved = true
			case "model_failed":
				judge.fail = true
			case "invalid_hit":
				fixture.receipt.Status = "completed"
				fixture.receipt.Owned = false
			}
			service := NewService(Flags{Rerank: true}, DefaultBudget(), judge)
			service.SetRerankStore(fixture)
			result := service.RerankCandidates(context.Background(), "synthetic", candidates)
			wantCalls := int32(0)
			if mode == "lost_response" || mode == "unconfirmed" || mode == "model_failed" {
				wantCalls = 1
			}
			if judge.calls.Load() != wantCalls || result.Applied != (mode == "lost_response") || len(result.Candidates) != 1 || result.Candidates[0].ID != "1" {
				t.Fatalf("calls=%d result=%+v", judge.calls.Load(), result)
			}
			if mode == "lost_response" && (fixture.gets != 1 || fixture.completes != 1 || result.CacheStatus != "miss") {
				t.Fatal("committed completion not recovered")
			}
			if mode == "unconfirmed" && (fixture.gets != 2 || fixture.completes != 2) {
				t.Fatal("bounded identical write retry not exercised")
			}
			if mode == "model_failed" && (fixture.completion.Status != "failed" || len(fixture.completion.Answers) != 0) {
				t.Fatal("failed model cached as success")
			}
			if mode == "missing_version" && fixture.claims != 0 {
				t.Fatal("missing identity reached remote cache")
			}
		})
	}
}
func TestRerankRejectsIncompleteRogueAndNonfiniteScores(t *testing.T) {
	for _, scores := range []map[string]classify.RawAnswer{
		{}, {"rogue": {Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: 2}}},
		{"rerank_1": {Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: math.NaN()}}},
		{"rerank_1": {Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: 4}}},
		{"rerank_1": {Type: classify.TypeNoul}},
	} {
		if _, err := rerankScores(cacheCandidates(), scores); err == nil {
			t.Fatal("invalid cache answers accepted")
		}
	}
}
