package processor

import (
	"context"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

type classificationAttemptStore interface {
	SubmitClassificationAttempts(context.Context, *cairn.ClassificationJob, []classify.ProviderCall) error
}

// Audit writes never retry inference. A successful result still commits its
// full raw calls if this append is unavailable; operators see the write error.
func (p *Processor) recordClassificationAttempts(ctx context.Context, job *cairn.ClassificationJob, calls []classify.ProviderCall, inferenceErr error) {
	store, ok := p.stages.queue.(classificationAttemptStore)
	if !ok {
		return
	}
	bounded := classify.AttemptReceiptCalls(calls, inferenceErr)
	if len(bounded) == 0 {
		return
	}
	reportCtx, cancel := boundedStateReportContext(ctx)
	defer cancel()
	err := retryEvidenceWrite(reportCtx, func() error { return store.SubmitClassificationAttempts(reportCtx, job, bounded) })
	if err != nil {
		p.logger.ErrorContext(reportCtx, "classification attempt receipt failed", "provider_calls", len(bounded), "error", err)
	}
}
