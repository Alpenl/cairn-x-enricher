package extension

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type budgetJudge struct {
	calls atomic.Int32
	wait  bool
	fail  bool
}

func (j *budgetJudge) Judge(ctx context.Context, _ any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	j.calls.Add(1)
	if j.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if j.fail {
		return nil, errors.New("synthetic provider unavailable")
	}
	answers := make(map[string]classify.RawAnswer)
	for id, q := range questions {
		switch q.Type {
		case classify.TypeScore:
			answers[id] = classify.RawAnswer{Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: 2}}
		case classify.TypeChoice:
			answers[id] = entityRelevance(.95)
		default:
			v := 0.95
			answers[id] = classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &v}}
		}
	}
	return answers, nil
}
func budgetCandidates(id string) []Candidate {
	return []Candidate{{ID: id, Text: "synthetic", Allowed: true}}
}

func TestServiceBudgetPersistsAcrossRequestsAndConcurrentKinds(t *testing.T) {
	j := &budgetJudge{}
	b := DefaultBudget()
	b.MaxCallsTotal = 3
	s := NewService(Flags{Entities: true, Rerank: true}, b, j)
	if !s.RerankCandidates(context.Background(), "query", budgetCandidates("1")).Applied {
		t.Fatal("first call failed")
	}
	if s.EntitiesForItem(context.Background(), 1, []Block{{ID: "one", Text: "Acme builds Widgets"}}, nil).State == EntityFailed {
		t.Fatal("entity call failed")
	}
	if s.RerankCandidates(context.Background(), "changed query", budgetCandidates("1")).Applied {
		t.Fatal("per-item cap reset by changed query or kind")
	}
	var wg sync.WaitGroup
	for n := 2; n < 32; n++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s.RerankCandidates(context.Background(), "q", budgetCandidates(fmt.Sprint(id)))
		}(n)
	}
	wg.Wait()
	if j.calls.Load() != 3 {
		t.Fatalf("global calls=%d want 3", j.calls.Load())
	}
}

func TestServiceTokenReservationsTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"tokens", "timeout", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			j := &budgetJudge{wait: mode == "timeout", fail: mode == "failure"}
			b := DefaultBudget()
			b.MaxCallsTotal = 1
			b.Timeout = 20 * time.Millisecond
			if mode == "tokens" {
				b.MaxTokens = InputTokenReservation - 1
			}
			s := NewService(Flags{Rerank: true}, b, j)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			start := time.Now()
			result := s.RerankCandidates(ctx, "q", budgetCandidates("1"))
			if result.Applied || !strings.Contains(result.Reason, "unavailable") {
				t.Fatalf("failure not explicit: %+v", result)
			}
			if time.Since(start) > time.Second {
				t.Fatal("timeout not applied")
			}
			if mode == "canceled" || mode == "tokens" {
				if j.calls.Load() != 0 {
					t.Fatal("called despite preflight refusal")
				}
			} else {
				s.RerankCandidates(context.Background(), "q", budgetCandidates("2"))
				if j.calls.Load() != 1 {
					t.Fatal("failure/timeout refunded or retried")
				}
			}
		})
	}
}

func TestEvidenceAndJudgmentsShareOneCallBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write([]byte("synthetic")) }))
	defer server.Close()
	j := &budgetJudge{}
	b := DefaultBudget()
	b.MaxCallsTotal = 1
	s := NewService(Flags{Evidence: true, Rerank: true}, b, j)
	s.RerankCandidates(context.Background(), "q", budgetCandidates("1"))
	out := s.RequestEvidenceForItem(context.Background(), 2, server.Client(), DefaultFetchPolicy(nil), server.URL)
	if out.State != "blocked" || !strings.Contains(out.Reason, "budget") || requests.Load() != 0 {
		t.Fatalf("evidence bypassed shared budget: %+v", out)
	}
}

func TestRealJudgePreflightRefusesUnknownCeilingAndOversizedState(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, model := range []string{"jev-latest", "jev-1.13.0"} {
		client, err := classify.NewClient(server.URL, "fixture", model, server.Client(), taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}})
		if err != nil {
			t.Fatal(err)
		}
		s := NewService(Flags{Entities: true}, DefaultBudget(), client)
		text := "Acme builds Widgets"
		if model == "jev-1.13.0" {
			text += strings.Repeat(" material", 100000)
		}
		result := s.EntitiesForItem(context.Background(), 1, []Block{{ID: "one", Text: text}}, nil)
		if result.State != EntityFailed || result.Calls != 0 {
			t.Fatalf("preflight accepted %s: %+v", model, result)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("preflight touched provider")
	}
}
