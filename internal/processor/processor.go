package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
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
	Classified           int64         `json:"classified"`
	ClassificationFailed int64         `json:"classification_failed"`
	StartedAt            time.Time     `json:"started_at"`
	Duration             time.Duration `json:"duration"`
	Claimed              int64         `json:"claimed"`
	Completed            int64         `json:"completed"`
	Failed               int64         `json:"failed"`
}

// HasWork reports whether the batch actually claimed or handled any job.
func (s Stats) HasWork() bool {
	return s.Claimed > 0 || s.Completed > 0 || s.Failed > 0 || s.Classified > 0 || s.ClassificationFailed > 0
}

// Processor leases and enriches jobs with bounded concurrency.
type Processor struct {
	stages      *stages
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
// Run processes up to maxJobs and stops early on queue infrastructure errors.
//
// Cancelling ctx stops the batch from claiming new work and bounds the run, but
// an already-claimed job is allowed to finish while its own request deadline
// holds. Cancelling the work itself would interrupt jobs mid-request and waste
// their lease, which is exactly what graceful shutdown is trying to avoid.
func (p *Processor) Run(ctx context.Context, maxJobs int) (Stats, error) {
	started := time.Now().UTC()
	stats := Stats{StartedAt: started}
	if maxJobs < 1 {
		stats.Duration = time.Since(started)
		return stats, nil
	}

	// claimCtx gates claiming only. workCtx is what in-flight jobs observe, and
	// it is detached from claimCtx so a cancelled batch finishes its work.
	claimCtx, stopClaiming := context.WithCancel(ctx)
	defer stopClaiming()
	workCtx := context.WithoutCancel(ctx)

	var claimed atomic.Int64
	var completed atomic.Int64
	var failed atomic.Int64
	var claimSlots atomic.Int64
	var firstErr error
	var errOnce sync.Once
	var workers sync.WaitGroup
	var classificationErr error
	classificationDone := make(chan struct{})
	go func() {
		defer close(classificationDone)
		defer RecoverJob(p.logger, "classification batch", 0, &classificationErr)
		stats.Classified, stats.ClassificationFailed, classificationErr = p.RunClassifications(ctx, maxJobs)
	}()

	recordFatal := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			stopClaiming()
		})
	}

	workerCount := min(p.concurrency, maxJobs)
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			// A panic in a batch worker would otherwise terminate the process
			// rather than failing a single job. Recovering here is not enough
			// on its own: the panic is also reported as a fatal batch error so
			// the operator sees it instead of a silently successful batch.
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				p.logger.Error("scheduled batch worker panicked",
					"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				recordFatal(fmt.Errorf("scheduled batch worker panicked: %v", recovered))
			}()
			for claimCtx.Err() == nil {
				if claimSlots.Add(1) > int64(maxJobs) {
					return
				}
				job, err := p.queue.Claim(claimCtx)
				if err != nil {
					// A cancelled claim context means the batch was told to stop,
					// not that the queue failed.
					if claimCtx.Err() != nil {
						return
					}
					recordFatal(fmt.Errorf("claim enrichment job: %w", err))
					return
				}
				if job == nil {
					return
				}
				claimed.Add(1)
				// Deliveries and failures are reported with the work context so
				// they still succeed for a job that was already claimed.
				if err := p.Process(workCtx, job); err != nil {
					failed.Add(1)
					return
				}
				completed.Add(1)
			}
		}()
	}
	workers.Wait()
	<-classificationDone
	if classificationErr == nil && ctx.Err() == nil && p.stages != nil {
		remaining := maxJobs - int(stats.Classified+stats.ClassificationFailed)
		if remaining > 0 {
			done, failed, err := p.RunClassifications(ctx, remaining)
			stats.Classified += done
			stats.ClassificationFailed += failed
			classificationErr = err
		}
	}

	stats.Claimed = claimed.Load()
	stats.Completed = completed.Load()
	stats.Failed = failed.Load()
	firstErr = errors.Join(firstErr, classificationErr)
	stats.Duration = time.Since(started)
	return stats, firstErr
}

func (p *Processor) processJob(ctx context.Context, job *cairn.Job, sourceText string) error {
	if p.stages != nil {
		return p.processStages(ctx, job, strings.TrimSpace(sourceText))
	}
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

	// The path decides what the result can be trusted for, so it is recorded on
	// every outcome. A search result establishes the post text from the source,
	// while the recovery path only reformats text that was already stored: it can
	// add a missing translation or summary, but it cannot detect that the stored
	// text was truncated or was never the requested post. Without this label an
	// operator cannot tell a fresh retrieval from a re-derivation of old data.
	path := failurePathSearch
	if useExisting {
		path = failurePathRecovered
	}
	logger = logger.With("path", string(path))

	result, err := p.enricher.Enrich(ctx, enrich.Input{
		ID:           job.ID,
		URL:          job.URL,
		Note:         job.Note,
		Attempt:      job.Attempt,
		SourceText:   sourceText,
		RelatedLinks: existing.RelatedURLs,
	})
	if err != nil {
		return p.reportFailure(ctx, logger, job, path, err)
	}

	images := []cairn.ImageRef{}
	if useExisting && len(existing.Images) > 0 {
		images = append(images, existing.Images...)
	}
	if len(result.ImageURLs) > 0 {
		images, err = p.queue.StoreImages(ctx, job.ID, job.LeaseToken, result.ImageURLs)
		if err != nil {
			return p.reportFailure(ctx, logger, job, path, fmt.Errorf("store enrichment images: %w", err))
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
		Classification:   &result.Classification,
	}
	if err := p.queue.Complete(ctx, job.ID, completion); err != nil {
		logger.ErrorContext(ctx, "failed to store enrichment", "error", err)
		return fmt.Errorf("store enrichment: %w", err)
	}
	logger.InfoContext(ctx, "enrichment completed",
		"related_links", len(result.RelatedLinks), "images", len(images),
		"original_text_bytes", len(result.OriginalText))
	if discarded := len(result.Classification.DiscardedTags); discarded > 0 {
		logger.WarnContext(ctx, "classification requires review", "discarded_tags", discarded)
	}
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

func (p *Processor) reportFailure(ctx context.Context, logger *slog.Logger, job *cairn.Job, path failurePathLabel, err error) error {
	if ctx.Err() != nil {
		logger.WarnContext(ctx, "enrichment interrupted", "error", ctx.Err())
		return ctx.Err()
	}
	// The stored message names the path, so a failure that came from reformatting
	// already-stored text is distinguishable from a genuine retrieval failure.
	// The two need different responses: only the latter means the bookmark's
	// content is still missing. It is prefixed rather than suffixed because the
	// Worker truncates the stored message.
	message := prefixFailurePath(path, boundedError(err))
	if reportErr := p.queue.Fail(ctx, job.ID, job.LeaseToken, message); reportErr != nil {
		logger.ErrorContext(ctx, "failed to report enrichment failure", "error", reportErr)
		return fmt.Errorf("report enrichment failure: %w", reportErr)
	}
	logger.WarnContext(ctx, "enrichment failed", "error", message)
	return err
}

// failurePathLabel describes where a failed enrichment got its input text.
// It is recorded because only a search failure means the bookmark's content is
// still missing; a recovery failure means the stored text could not be
// reformatted, which is a different problem with a different fix.
type failurePathLabel string

const (
	failurePathSearch    failurePathLabel = "search"
	failurePathRecovered failurePathLabel = "recovered_source"
)

// prefixFailurePath puts the path ahead of the cause so it survives the
// Worker's truncation of the stored failure message.
func prefixFailurePath(path failurePathLabel, cause string) string {
	return "[" + string(path) + "] " + cause
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
