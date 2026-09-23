// Command budget-probe checks the production Service across two independent
// rerank calls. Exit 1 is the known unsatisfied B09-T01 invariant, not success.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

type judge struct{ calls int }

func (j *judge) Judge(_ context.Context, _ any, _ map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	j.calls++
	return map[string]classify.RawAnswer{"rerank_one": {Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: 2}}}, nil
}
func main() {
	j := &judge{}
	flags := extension.DefaultFlags()
	flags.Rerank = true
	budget := extension.DefaultBudget()
	budget.MaxCallsTotal = 1
	service := extension.NewService(flags, budget, j)
	candidates := []extension.Candidate{{ID: "one", Text: "synthetic material", Allowed: true}}
	service.RerankCandidates(context.Background(), "synthetic query", candidates)
	if j.calls != 1 {
		fmt.Printf("FAIL: first request did not reach Judge exactly once: %d\n", j.calls)
		os.Exit(1)
	}
	service.RerankCandidates(context.Background(), "synthetic query", candidates)
	fmt.Printf("configured total calls=1; actual production Service judge calls=%d; paid calls=0\n", j.calls)
	if j.calls > budget.MaxCallsTotal {
		fmt.Println("FAIL: total budget resets between calls; B09-T01/B09-T10/SC30 remains incomplete")
		os.Exit(1)
	}
	fmt.Println("PASS: calls stayed within configured total")
}
