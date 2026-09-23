package classify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// CallBudgetLimits bounds each actual classification HTTP attempt per UTC day.
// It is separate from the evidence byte budget and the optional extensions.
type CallBudgetLimits struct {
	MaxCallsTotal    int `json:"max_calls_total"`
	MaxCallsPerItem  int `json:"max_calls_per_item"`
	MaxTokens        int `json:"max_tokens"`
	MaxTokensPerItem int `json:"max_tokens_per_item"`
}

// DefaultCallBudgetLimits is the server-enforced maximum; clients may tighten.
func DefaultCallBudgetLimits() CallBudgetLimits {
	return CallBudgetLimits{20, 5, 20 * 65536, 5 * 65536}
}

// Validate prevents caller-owned settings from widening the server ceiling.
func (b CallBudgetLimits) Validate() error {
	ceiling := DefaultCallBudgetLimits()
	if b.MaxCallsTotal < 1 || b.MaxCallsTotal > ceiling.MaxCallsTotal || b.MaxCallsPerItem < 1 || b.MaxCallsPerItem > ceiling.MaxCallsPerItem || b.MaxTokens < 1 || b.MaxTokens > ceiling.MaxTokens || b.MaxTokensPerItem < 1 || b.MaxTokensPerItem > ceiling.MaxTokensPerItem {
		return errors.New("classification budget exceeds server limits")
	}
	return nil
}

// ClassificationLease is the current canonical input and target binding.
type ClassificationLease struct {
	LinkID             int64  `json:"link_id"`
	LeaseToken         string `json:"lease_token"`
	Revision           int64  `json:"revision"`
	InputRevision      int64  `json:"input_revision"`
	TargetGeneration   int64  `json:"target_generation"`
	SpecID             string `json:"spec_id"`
	ContentRevision    int64  `json:"content_revision"`
	EvidenceSnapshotID int64  `json:"evidence_snapshot_id"`
	EvidenceHash       string `json:"evidence_hash"`
}

// CallReservation never contains source material or credentials.
type CallReservation struct {
	ClassificationLease
	OperationKey string           `json:"operation_key"`
	Model        string           `json:"model"`
	RequestHash  string           `json:"request_hash"`
	Tokens       int              `json:"tokens"`
	Limits       CallBudgetLimits `json:"limits"`
}

// CallGrant is an at-most-once permission, not a replayable authorization.
type CallGrant struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`
}

// CallBudgetStore persists admission across consumers and process restarts.
type CallBudgetStore interface {
	ReserveClassificationBudget(context.Context, CallReservation) (CallGrant, error)
}
type classificationLeaseKey struct{}

// WithClassificationLease attaches the already verified job identity to calls.
func WithClassificationLease(ctx context.Context, lease ClassificationLease) context.Context {
	return context.WithValue(ctx, classificationLeaseKey{}, lease)
}

// SetCallBudget enables required persistent admission in production clients.
// Configure once before starting workers. Offline research clients opt in separately.
func (c *Client) SetCallBudget(store CallBudgetStore, limits CallBudgetLimits) error {
	if store == nil {
		return errors.New("classification budget store is required")
	}
	if c.model != "jev-1.13.0" {
		return errors.New("classification budget requires verified pinned TYPESAFE_MODEL=jev-1.13.0 and a matching controlled target")
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	c.callBudgetStore = store
	c.callBudgetLimits = limits
	return nil
}
func (c *Client) reserveProviderCall(ctx context.Context, body []byte) error {
	if c.callBudgetStore == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lease, ok := ctx.Value(classificationLeaseKey{}).(ClassificationLease)
	if !ok || lease.LinkID < 1 || lease.LeaseToken == "" || lease.SpecID != c.spec.SpecID || lease.EvidenceSnapshotID < 1 || lease.EvidenceHash == "" {
		return enrich.Classified(errors.New("classification budget requires a verified leased snapshot"), enrich.ErrorClassConfiguration)
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	grant, err := c.callBudgetStore.ReserveClassificationBudget(ctx, CallReservation{ClassificationLease: lease, OperationKey: hex.EncodeToString(nonce), Model: c.model, RequestHash: sha256Hex(body), Tokens: 65536, Limits: c.callBudgetLimits})
	if err != nil {
		if enrich.IsStale(err) {
			return err
		}
		// An unknown grant must stop this component rather than draining jobs while
		// its budget backend is unavailable. No retry or refund at this boundary.
		return enrich.Classified(errors.New("classification budget unavailable; inference not attempted"), enrich.ErrorClassBudget)
	}
	if !grant.Granted || grant.Reason != "reserved" {
		return enrich.Classified(errors.New("classification budget not granted: "+grant.Reason), enrich.ErrorClassBudget)
	}
	return nil
}

// attemptedCall omits a pre-admission refusal from actual provider-call history.
func attemptedCall(call ProviderCall) []ProviderCall {
	if call.RequestHash == "" {
		return nil
	}
	return []ProviderCall{call}
}
