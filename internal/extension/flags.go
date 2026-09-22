// Package extension holds the bounded semantic extensions: entity extraction,
// evidence escalation, controlled reranking and taxonomy proposals.
//
// Every capability is off by default and shares bounded deployment limits. A failure or
// an exhausted budget degrades to the previous behaviour (no entities, the
// original order, no added evidence) rather than failing the bookmark.
package extension

import (
	"errors"
	"sync"
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

// Budget bounds calls and reserved input tokens per item and UTC day. User content or a model
// answer can never raise a budget or choose an arbitrary URL to execute.
type Budget struct {
	MaxCallsPerItem  int           `json:"max_calls_per_item"`
	MaxCallsTotal    int           `json:"max_calls_total"`
	MaxTokens        int           `json:"max_tokens"`
	MaxTokensPerItem int           `json:"max_tokens_per_item"`
	Timeout          time.Duration `json:"timeout"`
}

// DefaultBudget is deliberately small.
func DefaultBudget() Budget {
	return Budget{MaxCallsPerItem: 2, MaxCallsTotal: 20, MaxTokens: 20 * InputTokenReservation, MaxTokensPerItem: 2 * InputTokenReservation, Timeout: 20 * time.Second}
}

// InputTokenReservation reserves the entire documented Jev 1.13 input context
// per attempted call; output is free. It is not observed usage or a byte/token
// estimate. Unknown outcomes are never refunded. See docs.typesafe.ai/models.
const InputTokenReservation = 65536

// Ledger is a concurrency-safe process-local ceiling, shared by all extension
// requests. Production also reserves in Worker/D1 across processes and restarts.
type Ledger struct {
	mu     sync.Mutex
	budget Budget
	calls  int
	tokens int
	items  map[string][2]int
	day    string
}

// NewLedger creates a shared process-local ledger with UTC daily windows.
func NewLedger(budget Budget) *Ledger { return &Ledger{budget: budget, items: make(map[string][2]int)} }

// ErrBudgetExhausted is returned instead of silently making another call.
var ErrBudgetExhausted = errors.New("extension budget exhausted")

// Reserve books one call with a conservative input-token reservation, or fails.
func (l *Ledger) Reserve(tokens int) error {
	return l.ReserveItems(nil, tokens)
}

// ReserveItems atomically charges one call globally and once per distinct item.
// Charging each item the whole request is deliberately conservative for rerank.
func (l *Ledger) ReserveItems(items []string, tokens int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	if l.day != day {
		l.day = day
		l.calls, l.tokens = 0, 0
		l.items = make(map[string][2]int)
	}
	if tokens < 0 || l.calls >= l.budget.MaxCallsTotal || tokens > l.budget.MaxTokens-l.tokens {
		return ErrBudgetExhausted
	}
	seen := make(map[string]bool)
	itemTokens := l.budget.MaxTokensPerItem
	if itemTokens == 0 {
		itemTokens = l.budget.MaxTokens
	}
	for _, id := range items {
		if seen[id] {
			continue
		}
		seen[id] = true
		used := l.items[id]
		if used[0] >= l.budget.MaxCallsPerItem || tokens > itemTokens-used[1] {
			return ErrBudgetExhausted
		}
	}
	l.calls++
	l.tokens += tokens
	for id := range seen {
		used := l.items[id]
		l.items[id] = [2]int{used[0] + 1, used[1] + tokens}
	}
	return nil
}

// Calls reports the number of reserved calls.
func (l *Ledger) Calls() int { l.mu.Lock(); defer l.mu.Unlock(); return l.calls }

// Tokens reports the number of reserved tokens.
func (l *Ledger) Tokens() int { l.mu.Lock(); defer l.mu.Unlock(); return l.tokens }

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
