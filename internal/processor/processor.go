package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error)
	StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error)
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

// HasWork reports whether the batch actually claimed or handled any job.
func (s Stats) HasWork() bool {
	return s.Claimed > 0 || s.Completed > 0 || s.Failed > 0
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
	return p.ProcessWithSource(ctx, job, "")
}

// ProcessWithSource handles one job using caller-supplied source text instead of x_search.
func (p *Processor) ProcessWithSource(ctx context.Context, job *cairn.Job, sourceText string) error {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.processJob(ctx, job, sourceText)
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

func (p *Processor) processJob(ctx context.Context, job *cairn.Job, sourceText string) error {
	logger := p.logger.With("link_id", job.ID, "attempt", job.Attempt)
	logger.InfoContext(ctx, "enrichment started")
	var existing cairn.BookmarkDetail
	useExisting := false
	sourceText = strings.TrimSpace(sourceText)
	if sourceText == "" && job.Attempt > 1 {
		var detailErr error
		existing, detailErr = p.queue.GetBookmark(ctx, job.ID)
		if detailErr != nil {
			logger.WarnContext(ctx, "failed to inspect existing enrichment detail", "error", detailErr)
		} else if strings.TrimSpace(existing.OriginalText) != "" && needsTransform(existing.Bookmark) {
			sourceText = existing.OriginalText
			useExisting = true
			logger.InfoContext(ctx, "recovering partial enrichment from existing source text")
		}
	}

	result, err := p.enricher.Enrich(ctx, enrich.Input{
		ID:           job.ID,
		URL:          job.URL,
		Note:         job.Note,
		Attempt:      job.Attempt,
		SourceText:   sourceText,
		RelatedLinks: existing.RelatedURLs,
	})
	if err != nil {
		return p.reportFailure(ctx, logger, job, err)
	}

	images := []cairn.ImageRef{}
	if useExisting && len(existing.Images) > 0 {
		images = append(images, existing.Images...)
	}
	if len(result.ImageURLs) > 0 {
		images, err = p.queue.StoreImages(ctx, job.ID, job.LeaseToken, result.ImageURLs)
		if err != nil {
			return p.reportFailure(ctx, logger, job, fmt.Errorf("store enrichment images: %w", err))
		}
	}

	completion := cairn.Completion{
		LeaseToken:       job.LeaseToken,
		AITitle:          result.AITitle,
		OriginalLanguage: result.OriginalLanguage,
		OriginalText:     result.OriginalText,
		TranslatedText:   result.TranslatedText,
		Summary:          result.Summary,
		RelatedLinks:     relatedLinks(result.RelatedLinks, existing.RelatedURLs),
		Images:           images,
		Model:            result.Model,
	}
	if err := p.queue.Complete(ctx, job.ID, completion); err != nil {
		logger.ErrorContext(ctx, "failed to store enrichment", "error", err)
		return fmt.Errorf("store enrichment: %w", err)
	}
	logger.InfoContext(ctx, "enrichment completed", "related_links", len(result.RelatedLinks), "images", len(images))
	return nil
}

func needsTransform(bookmark cairn.Bookmark) bool {
	return bookmark.AITitle == "" || bookmark.OriginalLanguage == "" || bookmark.TranslatedText == "" || bookmark.Summary == ""
}

func relatedLinks(resultLinks, existingLinks []string) []string {
	if len(resultLinks) > 0 || len(existingLinks) == 0 {
		return resultLinks
	}
	return existingLinks
}

func (p *Processor) reportFailure(ctx context.Context, logger *slog.Logger, job *cairn.Job, err error) error {
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
