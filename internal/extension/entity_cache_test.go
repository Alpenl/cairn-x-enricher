package extension

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

type entityJudgeFixture struct{ fakeJudge }

func (f *entityJudgeFixture) PreparedJudgeRequest(state any, q map[string]classify.ProviderQuestion) ([]byte, error) {
	return json.Marshal(map[string]any{"model": "jev-1.13.0", "state": state, "questions": q})
}

type entityStoreFixture struct {
	receipt                    EntityReceipt
	claimErr                   error
	completeErr                bool
	unconfirmed                bool
	claims, completions, reads int
}

func (f *entityStoreFixture) ClaimEntity(_ context.Context, _ EntityClaim) (EntityReceipt, error) {
	f.claims++
	return f.receipt, f.claimErr
}
func (f *entityStoreFixture) CompleteEntity(_ context.Context, _ string, c EntityCompletion) (EntityReceipt, error) {
	f.completions++
	if !f.unconfirmed {
		f.receipt.Status = c.Status
		f.receipt.Answers = c.Answers
		f.receipt.Owned = false
	}
	if f.completeErr {
		return EntityReceipt{}, errors.New("lost response")
	}
	return f.receipt, nil
}
func (f *entityStoreFixture) GetEntity(_ context.Context, _ string) (EntityReceipt, error) {
	f.reads++
	return f.receipt, nil
}
func entityFixture(t *testing.T) (*Service, *entityJudgeFixture, *entityStoreFixture, EntityBinding) {
	t.Helper()
	j := &entityJudgeFixture{fakeJudge: fakeJudge{answers: map[string]classify.RawAnswer{"entity_0": entityRelevance(.9)}}}
	store := &entityStoreFixture{receipt: EntityReceipt{Key: strings.Repeat("a", 64), Status: "pending", Owned: true, Answers: map[string]classify.RawAnswer{}}}
	s := NewService(Flags{Entities: true}, DefaultBudget(), j)
	s.SetEntityStore(store)
	return s, j, store, EntityBinding{LinkID: 1, EvidenceSnapshotID: 1, ContentRevision: 1, ContentHash: strings.Repeat("b", 64)}
}
func TestEntityCacheDoesNotInferForUnavailablePendingFailedOrMalformedHits(t *testing.T) {
	for _, mode := range []string{"unavailable", "pending", "failed", "malformed", "unbound"} {
		t.Run(mode, func(t *testing.T) {
			s, j, store, b := entityFixture(t)
			store.receipt.Owned = false
			switch mode {
			case "unavailable":
				store.claimErr = errors.New("backend unavailable")
			case "failed":
				store.receipt.Status = "failed"
			case "malformed":
				store.receipt.Status = "completed"
			case "unbound":
				b.EvidenceSnapshotID = 0
			}
			result := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
			if result.State != EntityFailed || j.calls != 0 || store.completions != 0 {
				t.Fatalf("unexpected inference/result: %+v calls=%d", result, j.calls)
			}
			if mode == "unbound" && store.claims != 0 {
				t.Fatal("unbound input reached cache")
			}
		})
	}
}
func TestEntityCacheRecoversLostCompletionAndReusesEmptyWithoutBudget(t *testing.T) {
	s, j, store, b := entityFixture(t)
	j.answers = map[string]classify.RawAnswer{"entity_0": entityRelevance(.1)}
	store.completeErr = true
	first := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
	if first.State != EntityCompletedEmpty || first.OperationKey == "" || j.calls != 1 || store.reads != 1 {
		t.Fatalf("lost acknowledgment not recovered: %+v", first)
	}
	budget := DefaultBudget()
	budget.MaxTokens = 1
	s = NewService(Flags{Entities: true}, budget, j)
	s.SetEntityStore(store)
	second := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
	if second.State != EntityCompletedEmpty || second.Calls != 0 || first.OperationKey != second.OperationKey || j.calls != 1 {
		t.Fatalf("empty reuse charged or changed receipt: %+v", second)
	}
}
func TestEntityCacheUnconfirmedSaveNeverRetriesModel(t *testing.T) {
	s, j, store, b := entityFixture(t)
	store.completeErr = true
	store.unconfirmed = true
	result := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
	if result.State != EntityFailed || result.OperationKey != "" || j.calls != 1 || store.completions != 2 || store.reads != 2 {
		t.Fatalf("unconfirmed result %+v provider=%d saves=%d reads=%d", result, j.calls, store.completions, store.reads)
	}
}
func TestEntityCacheProviderFailurePersistsFailureAndDoesNotRetry(t *testing.T) {
	s, j, store, b := entityFixture(t)
	j.err = errors.New("provider unavailable")
	first := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
	second := s.EntitiesForSnapshot(context.Background(), b, []Block{{ID: "source", Text: "Acme"}}, nil)
	if first.State != EntityFailed || second.State != EntityFailed || j.calls != 1 || store.receipt.Status != "failed" {
		t.Fatalf("failure was retried: %+v %+v calls=%d", first, second, j.calls)
	}
}
