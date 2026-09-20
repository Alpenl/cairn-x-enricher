// Package extension holds the bounded semantic extensions: entity extraction,
// evidence escalation, controlled reranking and taxonomy proposals.
//
// Every capability is off by default and independently budgeted. A failure or
// an exhausted budget degrades to the previous behaviour (no entities, the
// original order, no added evidence) rather than failing the bookmark.
package extension

import (
	"errors"
	"time"
)

// Flags enables the extensions individually. All default to false so a normal
// classification never depends on an extension being available.
type Flags struct {
	Entities bool `json:"entities"`
	Evidence bool `json:"evidence"`
	Rerank   bool `json:"rerank"`
	Proposal bool `json:"proposal"`
}

// DefaultFlags keeps every extension off.
func DefaultFlags() Flags { return Flags{} }

// Budget bounds calls and tokens per item and in total. User content or a model
// answer can never raise a budget or choose an arbitrary URL to execute.
type Budget struct {
	MaxCallsPerItem int           `json:"max_calls_per_item"`
	MaxCallsTotal   int           `json:"max_calls_total"`
	MaxTokens       int           `json:"max_tokens"`
	Timeout         time.Duration `json:"timeout"`
}

// DefaultBudget is deliberately small.
func DefaultBudget() Budget {
	return Budget{MaxCallsPerItem: 2, MaxCallsTotal: 20, MaxTokens: 4000, Timeout: 20 * time.Second}
}

// Ledger tracks consumption against a budget. It is not safe for concurrent
// use; the caller serialises per batch.
type Ledger struct {
	budget Budget
	calls  int
	tokens int
}

// NewLedger creates a budget ledger for one bounded batch.
func NewLedger(budget Budget) *Ledger { return &Ledger{budget: budget} }

// ErrBudgetExhausted is returned instead of silently making another call.
var ErrBudgetExhausted = errors.New("extension budget exhausted")

// Reserve books one call with an estimated token cost, or fails.
func (l *Ledger) Reserve(tokens int) error {
	if l.calls+1 > l.budget.MaxCallsTotal || l.tokens+tokens > l.budget.MaxTokens {
		return ErrBudgetExhausted
	}
	l.calls++
	l.tokens += tokens
	return nil
}

// Calls reports the number of reserved calls.
func (l *Ledger) Calls() int { return l.calls }

// Tokens reports the number of reserved tokens.
func (l *Ledger) Tokens() int { return l.tokens }

// DedupeKey identifies one extension operation so a retry replays the stored
// result instead of paying again.
type DedupeKey struct {
	LinkID          int64  `json:"link_id"`
	Kind            string `json:"kind"`
	ContentRevision int64  `json:"content_revision"`
	// Scope distinguishes an operation over one span from one over the whole
	// source, so the two cannot collide.
	Scope string `json:"scope"`
}

func (k DedupeKey) String() string {
	return k.Kind + ":" + k.Scope + ":" + itoa(k.LinkID) + ":" + itoa(k.ContentRevision)
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buffer[index] = '-'
	}
	return string(buffer[index:])
}
