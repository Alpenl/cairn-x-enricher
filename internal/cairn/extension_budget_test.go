package cairn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

type reservationTestJudge struct{ calls atomic.Int32 }

func (*reservationTestJudge) JudgeInputReservation(any, map[string]classify.ProviderQuestion) (int, error) {
	return extension.InputTokenReservation, nil
}
func (j *reservationTestJudge) Judge(context.Context, any, map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	j.calls.Add(1)
	return map[string]classify.RawAnswer{"rerank_1": {Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: 2}}}, nil
}

func TestPersistentBudgetMustExplicitlyGrantBeforeModelCall(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   int32
	}{
		{"grant", 200, `{"granted":true,"reason":"reserved"}`, 1},
		{"legacy", 404, `{"error":"not_found"}`, 0},
		{"unauthorized", 401, `{"error":"unauthorized"}`, 0},
		{"unavailable", 503, `{"error":"unavailable"}`, 0},
		{"duplicate", 200, `{"granted":false,"reason":"already_reserved"}`, 0},
		{"exhausted", 200, `{"granted":false,"reason":"budget_exhausted"}`, 0},
		{"truncated", 200, `{"granted":true`, 0},
		{"wrong acknowledgment", 200, `{"granted":true,"reason":"already_reserved"}`, 0},
		{"missing grant", 200, `{"reason":"reserved"}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v2/extension-budget/reserve" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("wrong reservation route/auth")
				}
				var request extension.Reservation
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.OperationKey) != 64 || request.Kind != "rerank" || len(request.ItemIDs) != 1 || request.ItemIDs[0] != 1 || request.Tokens != 65536 {
					t.Error("wrong reservation identity or units")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			judge := &reservationTestJudge{}
			service := extension.NewService(extension.Flags{Rerank: true}, extension.DefaultBudget(), judge)
			service.SetBudgetStore(NewClient(server.URL, "fixture", server.Client()))
			result := service.RerankCandidates(context.Background(), "private synthetic query", []extension.Candidate{{ID: "1", Text: "synthetic", Allowed: true}})
			if calls.Load() != 1 || judge.calls.Load() != tt.want || result.Applied != (tt.want == 1) {
				t.Fatalf("grant bypass/retry: requests=%d judgments=%d applied=%v", calls.Load(), judge.calls.Load(), result.Applied)
			}
		})
	}
}
