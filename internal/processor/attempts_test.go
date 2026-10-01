package processor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
)

type attemptQueue struct {
	*stageQueue
	receipts        [][]classify.ProviderCall
	receiptContexts []error
}

func (q *attemptQueue) SubmitClassificationAttempts(ctx context.Context, _ *cairn.ClassificationJob, calls []classify.ProviderCall) error {
	q.receipts = append(q.receipts, calls)
	q.receiptContexts = append(q.receiptContexts, ctx.Err())
	return nil
}

type failingAttemptClassifier struct{ calls *int }

func (f failingAttemptClassifier) SpecID() string { return "fixture" }
func (f failingAttemptClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	*f.calls++
	return classify.Result{RawJudgments: classify.RawJudgments{Calls: []classify.ProviderCall{{ReservationKey: "actual", RequestHash: "request", UsageMissing: true, HTTPStatus: 200}}}}, enrich.Classified(errors.New("private provider schema"), enrich.ErrorClassContract)
}

func TestContractFailurePersistsActualReceiptBeforeComponentPauseAndLogsCount(t *testing.T) {
	q := &attemptQueue{stageQueue: &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}}
	calls := 0
	var logs bytes.Buffer
	p := NewStaged(q, nil, failingAttemptClassifier{&calls}, "", "", slog.New(observability.SafeJSONHandler(&logs, slog.LevelDebug)), 1)
	_, _, err := p.RunClassifications(context.Background(), 20)
	if !errors.Is(err, ErrComponentPaused) || calls != 1 || len(q.receipts) != 1 || q.receiptContexts[0] != nil || q.receipts[0][0].ErrorClass != "contract_fault" || q.classificationFailures != 0 {
		t.Fatalf("lost audit or retried paid inference: calls=%d receipts=%v err=%v", calls, q.receipts, err)
	}
	if !strings.Contains(logs.String(), `"provider_calls":1`) || strings.Contains(logs.String(), "private provider schema") {
		t.Fatalf("numeric receipt log missing or leaked error: %s", logs.String())
	}
}
