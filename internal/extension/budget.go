package extension

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// ReservationLimits are trusted configuration, not model/user instructions.
// Worker applies its own ceiling even if a client requests larger values.
type ReservationLimits struct {
	MaxCallsTotal    int `json:"max_calls_total"`
	MaxCallsPerItem  int `json:"max_calls_per_item"`
	MaxTokens        int `json:"max_tokens"`
	MaxTokensPerItem int `json:"max_tokens_per_item"`
}

// Reservation carries only random operation identity, numeric bookmark IDs,
// kind and bounded counters. It contains no private query or source material.
type Reservation struct {
	OperationKey string            `json:"operation_key"`
	Kind         string            `json:"kind"`
	ItemIDs      []int64           `json:"item_ids"`
	Tokens       int               `json:"tokens"`
	Limits       ReservationLimits `json:"limits"`
}

// Grant is deliberately not replayable. A repeated or lost reservation may
// consume budget without an external call, but can never grant a second call.
type Grant struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`
}

// BudgetStore atomically persists the cross-process deployment-wide budget.
type BudgetStore interface {
	ReserveExtensionBudget(context.Context, Reservation) (Grant, error)
}

// SetBudgetStore is startup-only. Production always sets the actual Worker
// client; an unsupported endpoint fails closed, never falling back to memory.
func (s *Service) SetBudgetStore(store BudgetStore) { s.store = store }

func (s *Service) reserve(ctx context.Context, kind string, items []string, tokens int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids := make([]int64, 0, len(items))
	seen := make(map[int64]bool)
	if s.store != nil {
		for _, item := range items {
			id, err := strconv.ParseInt(item, 10, 64)
			if err != nil || id < 1 {
				return errors.New("persistent extension budget requires bookmark identity")
			}
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		if len(ids) < 1 || len(ids) > 20 {
			return errors.New("invalid extension budget item count")
		}
	}
	if err := s.ledger.ReserveItems(items, tokens); err != nil {
		return err
	}
	if s.store == nil {
		return ctx.Err()
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	itemTokens := s.Budget.MaxTokensPerItem
	if itemTokens == 0 {
		itemTokens = s.Budget.MaxTokens
	}
	grant, err := s.store.ReserveExtensionBudget(ctx, Reservation{
		OperationKey: hex.EncodeToString(key), Kind: kind, ItemIDs: ids, Tokens: tokens,
		Limits: ReservationLimits{s.Budget.MaxCallsTotal, s.Budget.MaxCallsPerItem, s.Budget.MaxTokens, itemTokens},
	})
	if err != nil {
		return errors.New("persistent extension budget unavailable")
	}
	if !grant.Granted {
		return ErrBudgetExhausted
	}
	return ctx.Err()
}

type reservationJudge interface {
	JudgeInputReservation(any, map[string]classify.ProviderQuestion) (int, error)
}

func (s *Service) judgeBounded(ctx context.Context, kind string, items []string, state any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, int, int, error) {
	if s.Budget.Timeout <= 0 {
		return nil, 0, 0, errors.New("invalid extension timeout")
	}
	callCtx, cancel := context.WithTimeout(ctx, s.Budget.Timeout)
	defer cancel()
	tokens := InputTokenReservation
	if preflight, ok := s.Judge.(reservationJudge); ok {
		var err error
		tokens, err = preflight.JudgeInputReservation(state, questions)
		if err != nil {
			return nil, 0, 0, err
		}
	} else if s.store != nil {
		return nil, 0, 0, errors.New("judge has no verified input token ceiling")
	}
	if tokens != InputTokenReservation {
		return nil, 0, 0, errors.New("unsupported extension token reservation")
	}
	if err := s.reserve(callCtx, kind, items, tokens); err != nil {
		return nil, 0, 0, err
	}
	answers, err := s.Judge.Judge(callCtx, state, questions)
	if err == nil {
		err = callCtx.Err()
	}
	return answers, 1, tokens, err
}
