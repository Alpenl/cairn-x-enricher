package processor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

const maxFailureMessageBytes = 1_800

// Queue leases work and conditionally stores outcomes.
type Queue interface {
	Claim(context.Context) (*cairn.Job, error)
	Complete(context.Context, int64, cairn.Completion) error
	Fail(context.Context, int64, string, string) error
}

// Stats summarizes one bounded processing batch.
type Stats struct {
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Claimed   int64         `json:"claimed"`
	Completed int64         `json:"completed"`
	Failed    int64         `json:"failed"`
}

// Processor leases and enriches jobs with bounded concurrency.
type Processor struct {
	queue       Queue
	enricher    enrich.Enricher
	logger      *slog.Logger
	concurrency int
	slots       chan struct{}
}

// New creates a Processor over a queue and enrichment workflow.
func New(queue Queue, enricher enrich.Enricher, logger *slog.Logger, concurrency int) *Processor {
	return &Processor{
		queue:       queue,
		enricher:    enricher,
		logger:      logger,
		concurrency: concurrency,
		slots:       make(chan struct{}, concurrency),
	}
}

// Process handles one already-leased job while respecting the shared concurrency limit.
func (p *Processor) Process(ctx context.Context, job *cairn.Job) error {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.processJob(ctx, job)
}

// Run processes up to maxJobs and stops early on queue infrastructure errors.
func (p *Processor) Run(ctx context.Context, maxJobs int) (Stats, error) {
	started := time.Now().UTC()
	stats := Stats{StartedAt: started}
	if maxJobs < 1 {
		stats.Duration = time.Since(started)
		return stats, nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var claimed atomic.Int64
	var completed atomic.Int64
	var failed atomic.Int64
	var claimSlots atomic.Int64
	var firstErr error
	var errOnce sync.Once
	var workers sync.WaitGroup

	recordFatal := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	workerCount := min(p.concurrency, maxJobs)
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for runCtx.Err() == nil {
				if claimSlots.Add(1) > int64(maxJobs) {
					return
				}
				job, err := p.queue.Claim(runCtx)
				if err != nil {
					recordFatal(fmt.Errorf("claim enrichment job: %w", err))
					return
				}
				if job == nil {
					return
				}
				claimed.Add(1)
				if err := p.Process(runCtx, job); err != nil {
					failed.Add(1)
					return
				}
				completed.Add(1)
			}
		}()
	}
	workers.Wait()

	stats.Claimed = claimed.Load()
	stats.Completed = completed.Load()
	stats.Failed = failed.Load()
	stats.Duration = time.Since(started)
	return stats, firstErr
}

func (p *Processor) processJob(ctx context.Context, job *cairn.Job) error {
	logger := p.logger.With("link_id", job.ID, "attempt", job.Attempt)
	logger.InfoContext(ctx, "enrichment started")
	result, err := p.enricher.Enrich(ctx, enrich.Input{
		ID:      job.ID,
		URL:     job.URL,
		Note:    job.Note,
		Attempt: job.Attempt,
	})
	if err != nil {
		if ctx.Err() != nil {
			logger.WarnContext(ctx, "enrichment interrupted", "error", ctx.Err())
			return ctx.Err()
		}
		message := boundedError(err)
		if reportErr := p.queue.Fail(ctx, job.ID, job.LeaseToken, message); reportErr != nil {
			logger.ErrorContext(ctx, "failed to report enrichment failure", "error", reportErr)
			return fmt.Errorf("report enrichment failure: %w", reportErr)
		}
		logger.WarnContext(ctx, "enrichment failed", "error", message)
		return err
	}

	completion := cairn.Completion{
		LeaseToken:   job.LeaseToken,
		OriginalText: result.OriginalText,
		Summary:      result.Summary,
		RelatedLinks: result.RelatedLinks,
		Model:        result.Model,
	}
	if err := p.queue.Complete(ctx, job.ID, completion); err != nil {
		logger.ErrorContext(ctx, "failed to store enrichment", "error", err)
		return fmt.Errorf("store enrichment: %w", err)
	}
	logger.InfoContext(ctx, "enrichment completed", "related_links", len(result.RelatedLinks))
	return nil
}

func boundedError(err error) string {
	message := err.Error()
	if len(message) <= maxFailureMessageBytes {
		return message
	}
	message = message[:maxFailureMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
