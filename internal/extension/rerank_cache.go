package extension

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// CacheItem binds a candidate to source, reading, decision and human versions.
type CacheItem struct {
	ID                   int64 `json:"id"`
	ContentRevision      int64 `json:"content_revision"`
	BodyRevision         int64 `json:"body_revision"`
	PersonalRevision     int64 `json:"personal_revision"`
	LatestDecisionID     int64 `json:"latest_decision_id"`
	LatestEntityRevision int64 `json:"latest_entity_revision"`
}

// RerankClaim contains private provider state; never include it in public logs.
type RerankClaim struct {
	OwnerToken  string      `json:"owner_token"`
	RequestJSON string      `json:"request_json"`
	ScopeHash   string      `json:"scope_hash"`
	SpecHash    string      `json:"spec_hash"`
	Items       []CacheItem `json:"items"`
}

// RerankReceipt is durable even when completion's response is lost.
type RerankReceipt struct {
	Key       string                        `json:"key"`
	Status    string                        `json:"status"`
	Owned     bool                          `json:"owned"`
	ExpiresAt int64                         `json:"expires_at"`
	Answers   map[string]classify.RawAnswer `json:"answers"`
}

// RerankCompletion cannot authorize a model call; only the owner can finish.
type RerankCompletion struct {
	OwnerToken string                        `json:"owner_token"`
	Status     string                        `json:"status"`
	Answers    map[string]classify.RawAnswer `json:"answers"`
}

// RerankStore owns private results with atomic ownership and version CAS.
type RerankStore interface {
	ClaimRerank(context.Context, RerankClaim) (RerankReceipt, error)
	CompleteRerank(context.Context, string, RerankCompletion) (RerankReceipt, error)
	GetRerank(context.Context, string) (RerankReceipt, error)
}

// SetRerankStore is startup-only. Unsupported backends fail without inference.
func (s *Service) SetRerankStore(store RerankStore) { s.rerankStore = store }

// HasRerankStore tells the HTTP surface to require canonical candidate versions.
func (s *Service) HasRerankStore() bool { return s.rerankStore != nil }

type preparedJudge interface {
	PreparedJudgeRequest(any, map[string]classify.ProviderQuestion) ([]byte, error)
}

func digestBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func rerankScores(candidates []Candidate, answers map[string]classify.RawAnswer) ([]RerankScore, error) {
	if len(candidates) != len(answers) {
		return nil, errors.New("incomplete rerank answer set")
	}
	scores := make([]RerankScore, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		answer, ok := answers["rerank_"+candidate.ID]
		if !ok || seen[candidate.ID] || answer.Type != classify.TypeScore || answer.Score == nil || math.IsNaN(answer.Score.Score) || math.IsInf(answer.Score.Score, 0) || answer.Score.Score < 0 || answer.Score.Score > 3 {
			return nil, errors.New("invalid rerank answer")
		}
		seen[candidate.ID] = true
		scores = append(scores, RerankScore{ID: candidate.ID, Score: int(answer.Score.Score*100 + 0.5)})
	}
	return scores, nil
}

func (s *Service) cachedRerank(ctx context.Context, scope string, candidates []Candidate, state any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, string, error) {
	preparer, ok := s.Judge.(preparedJudge)
	if !ok {
		return nil, "unavailable", errors.New("rerank provider cannot prepare an auditable request")
	}
	wire, err := preparer.PreparedJudgeRequest(state, questions)
	if err != nil {
		return nil, "unavailable", err
	}
	items := make([]CacheItem, 0, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.CacheItem == nil || candidate.CacheItem.ID < 1 || strconv.FormatInt(candidate.CacheItem.ID, 10) != candidate.ID {
			return nil, "unavailable", errors.New("rerank candidate version unavailable")
		}
		items = append(items, *candidate.CacheItem)
		ids = append(ids, candidate.ID)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "unavailable", err
	}
	owner := hex.EncodeToString(key)
	spec, _ := json.Marshal(map[string]any{"template": rerankInstructionTemplate, "criteria": rerankCriteria})
	claim := RerankClaim{OwnerToken: owner, RequestJSON: string(wire), ScopeHash: scope, SpecHash: digestBytes(spec), Items: items}
	ctx, stop := context.WithTimeout(ctx, s.Budget.Timeout)
	defer stop()
	receipt, err := s.rerankStore.ClaimRerank(ctx, claim)
	if err != nil {
		return nil, "unavailable", errors.New("rerank cache unavailable or candidates changed")
	}
	if receipt.Status == "completed" {
		return receipt.Answers, "hit", nil
	}
	if receipt.Status != "pending" || !receipt.Owned {
		return nil, receipt.Status, errors.New("rerank result is pending or previously failed")
	}
	answers, _, _, judgeErr := s.judgeBounded(ctx, "rerank", ids, state, questions)
	if judgeErr == nil {
		_, judgeErr = rerankScores(candidates, answers)
	}
	completion := RerankCompletion{OwnerToken: owner, Status: "completed", Answers: answers}
	if judgeErr != nil {
		completion.Status = "failed"
		completion.Answers = map[string]classify.RawAnswer{}
	}
	// Persist through cancellation, retrying only the same idempotent write.
	// A crash before persistence leaves pending ownership until fixed expiry.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var saved RerankReceipt
	for attempt := 0; attempt < 2; attempt++ {
		saved, err = s.rerankStore.CompleteRerank(saveCtx, receipt.Key, completion)
		if err == nil {
			break
		}
		recovered, readErr := s.rerankStore.GetRerank(saveCtx, receipt.Key)
		if readErr == nil && recovered.Status == completion.Status {
			saved, err = recovered, nil
			break
		}
		if saveCtx.Err() != nil {
			break
		}
	}
	if judgeErr != nil {
		return nil, "failed", judgeErr
	}
	if err != nil || saved.Status != "completed" {
		return nil, "unavailable", errors.New("rerank result not durably confirmed")
	}
	return saved.Answers, "miss", nil
}
