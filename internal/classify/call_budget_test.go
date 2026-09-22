package classify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

type callBudgetFixture struct {
	mu             sync.Mutex
	granted, limit int
	requests       []CallReservation
	fail           bool
}

func (f *callBudgetFixture) ReserveClassificationBudget(_ context.Context, r CallReservation) (CallGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.fail {
		return CallGrant{}, io.ErrUnexpectedEOF
	}
	if f.granted >= f.limit {
		return CallGrant{Reason: "budget_exhausted"}, nil
	}
	f.granted++
	return CallGrant{Granted: true, Reason: "reserved"}, nil
}
func budgetedClassification(t *testing.T, limit int) (*Client, *callBudgetFixture, context.Context, *[]string) {
	t.Helper()
	store := &callBudgetFixture{limit: limit}
	bodies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		bodies = append(bodies, string(raw))
		var request struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
			return
		}
		answers := map[string]any{}
		all := wireAnswers()
		for id := range request.Questions {
			answers[id] = all[id]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}})
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetCallBudget(store, DefaultCallBudgetLimits()); err != nil {
		t.Fatal(err)
	}
	ctx := WithClassificationLease(context.Background(), ClassificationLease{LinkID: 1, LeaseToken: "fixture-lease", Revision: 1, InputRevision: 1, TargetGeneration: 1, SpecID: client.SpecID(), ContentRevision: 1, EvidenceSnapshotID: 1, EvidenceHash: "fixture"})
	return client, store, ctx, &bodies
}
func TestClassificationBudgetCountsActualBatchesWithoutInventingDeniedCalls(t *testing.T) {
	client, store, ctx, bodies := budgetedClassification(t, 2)
	raw, err := client.EvaluateBatched(ctx, Input{OriginalText: "synthetic source"}, 2)
	if enrich.ClassOf(err) != enrich.ErrorClassBudget || raw.Coverage != "partial" || len(raw.Calls) != 2 || len(*bodies) != 2 || store.granted != 2 || len(store.requests) != 3 {
		t.Fatalf("err=%v coverage=%s calls=%d HTTP=%d granted=%d requests=%d", err, raw.Coverage, len(raw.Calls), len(*bodies), store.granted, len(store.requests))
	}
	for i, body := range *bodies {
		if store.requests[i].RequestHash != sha256Hex([]byte(body)) || store.requests[i].Tokens != 65536 {
			t.Fatal("reservation not bound to actual HTTP bytes")
		}
	}
	if store.requests[0].OperationKey == store.requests[1].OperationKey {
		t.Fatal("distinct HTTP requests reused grant")
	}
}
func TestClassificationBudgetFullReuseIsFreeAndPartialReuseConsumesOneCall(t *testing.T) {
	client, store, ctx, bodies := budgetedClassification(t, 2)
	first, err := client.Evaluate(ctx, Input{OriginalText: "synthetic source"})
	if err != nil {
		t.Fatal(err)
	}
	first.SourceRunID = 1
	reused, err := client.ClassifyReusing(ctx, Input{OriginalText: "synthetic source"}, &first, "single-request")
	if err != nil || len(reused.RawJudgments.Calls) != 0 || len(store.requests) != 1 || len(*bodies) != 1 {
		t.Fatalf("full reuse charged: %v %+v", err, reused.RawJudgments)
	}
	client.spec.Questions[0].Instructions = json.RawMessage(`"changed relevance criterion"`)
	partial, err := client.ClassifyReusing(ctx, Input{OriginalText: "synthetic source"}, &first, "single-request")
	if err != nil || len(partial.RawJudgments.Calls) != 1 || len(*bodies) != 2 || len(store.requests) != 2 {
		t.Fatalf("partial reuse budget err=%v calls=%d", err, len(*bodies))
	}
	var sent struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte((*bodies)[1]), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Questions) != 1 {
		t.Fatalf("changed one question but inferred %d", len(sent.Questions))
	}
	denied, err := client.Classify(ctx, Input{OriginalText: "changed source"})
	if !enrich.PausesComponent(err) || len(denied.RawJudgments.Calls) != 0 || len(*bodies) != 2 {
		t.Fatal("exhaustion retried or invented a provider call")
	}
}
func TestClassificationBudgetDenialMissingLeaseAndUnknownGrantMakeNoHTTPCall(t *testing.T) {
	for _, mode := range []string{"denied", "lost_ack", "missing_lease", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			client, store, ctx, bodies := budgetedClassification(t, 0)
			store.fail = mode == "lost_ack"
			if mode == "missing_lease" {
				ctx = context.Background()
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			raw, err := client.Evaluate(ctx, Input{OriginalText: "synthetic source"})
			if err == nil || len(raw.Calls) != 0 || len(*bodies) != 0 {
				t.Fatalf("err=%v calls=%d HTTP=%d", err, len(raw.Calls), len(*bodies))
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if (mode == "missing_lease" || mode == "canceled") && len(store.requests) != 0 {
				t.Fatal("invalid preflight reached admission")
			}
			if mode == "lost_ack" && len(store.requests) != 1 {
				t.Fatal("grant request retried")
			}
		})
	}
}
func TestClassificationBudgetOnlyAcceptsVerifiedPinnedModelAndLimits(t *testing.T) {
	client, store, _, _ := budgetedClassification(t, 1)
	client.model = "jev-latest"
	if err := client.SetCallBudget(store, DefaultCallBudgetLimits()); err == nil {
		t.Fatal("alias accepted")
	}
	for _, limits := range []CallBudgetLimits{{21, 5, 20 * 65536, 5 * 65536}, {20, 6, 20 * 65536, 5 * 65536}, {20, 5, 20*65536 + 1, 5 * 65536}, {20, 5, 20 * 65536, 0}} {
		if limits.Validate() == nil {
			t.Fatal("widened/zero limit accepted")
		}
	}
}
