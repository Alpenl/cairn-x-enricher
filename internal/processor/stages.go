package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// StageQueue persists source checkpoints and independent classification leases.
type StageQueue interface {
	Queue
	GetSource(context.Context, int64) (*enrich.Source, error)
	SaveSource(context.Context, int64, string, enrich.Source) error
	ClaimClassification(context.Context, string, string) (*cairn.ClassificationJob, error)
	CompleteClassification(context.Context, *cairn.ClassificationJob, classify.Result) error
	FailClassification(context.Context, *cairn.ClassificationJob, string) error
}

// SourceReader separates retrieval from generation of reading aids.
type SourceReader interface {
	FetchSource(context.Context, enrich.Input) (enrich.Source, error)
	Transform(context.Context, enrich.Input) (enrich.Result, error)
}

// Classifier judges stored evidence without retrieving or modifying it.
type Classifier interface {
	Classify(context.Context, classify.Input) (classify.Result, error)
}

type stages struct {
	queue                  StageQueue
	reader                 SourceReader
	classifier             Classifier
	version, model         string
	classificationDeadline time.Duration
}

// DefaultClassificationDeadline bounds one already-leased classification so a
// graceful shutdown cannot wait forever on a detached work context.
const DefaultClassificationDeadline = 3 * time.Minute

// NewStaged creates the production processor with independent semantic work.
func NewStaged(queue StageQueue, reader SourceReader, classifier Classifier, version, model string, logger *slog.Logger, concurrency int) *Processor {
	p := New(queue, nil, logger, concurrency)
	p.stages = &stages{queue: queue, reader: reader, classifier: classifier, version: version, model: model,
		classificationDeadline: DefaultClassificationDeadline}
	return p
}

func (p *Processor) processStages(ctx context.Context, job *cairn.Job, manual string) error {
	s := p.stages
	logger := p.logger.With("link_id", job.ID, "attempt", job.Attempt)
	input := enrich.Input{ID: job.ID, URL: job.URL, Note: job.Note, Attempt: job.Attempt, SourceText: manual}
	var source *enrich.Source
	var err error
	// Explicit manual text replaces a snapshot. Ordinary reruns reuse it.
	if manual == "" {
		source, err = s.queue.GetSource(ctx, job.ID)
		if err != nil {
			return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
		}
		if source == nil {
			// Adopt legacy saved text without retrieving it again.
			detail, readErr := s.queue.GetBookmark(ctx, job.ID)
			if readErr != nil {
				return p.reportFailure(ctx, logger, job, failurePathRecovered, readErr)
			}
			if detail.OriginalText != "" {
				source = &enrich.Source{OriginalText: detail.OriginalText, OriginalLanguage: detail.OriginalLanguage,
					Model: "legacy_saved", RelatedLinks: detail.RelatedURLs, ImageURLs: []string{}}
				// Register source without discarding already stored image refs below.
				if source.RelatedLinks == nil {
					source.RelatedLinks = []string{}
				}
				if err = s.queue.SaveSource(ctx, job.ID, job.LeaseToken, *source); err != nil {
					return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
				}
				return p.finishReading(ctx, job, *source, detail.Images)
			}
		}
	}
	if source == nil {
		fetched, fetchErr := s.reader.FetchSource(ctx, input)
		if fetchErr != nil {
			return p.reportFailure(ctx, logger, job, failurePathSearch, fetchErr)
		}
		source = &fetched
		if err = s.queue.SaveSource(ctx, job.ID, job.LeaseToken, *source); err != nil {
			return p.reportFailure(ctx, logger, job, failurePathSearch, err)
		}
		logger.InfoContext(ctx, "source saved; classification queued")
	}
	return p.finishReading(ctx, job, *source, nil)
}

func (p *Processor) finishReading(ctx context.Context, job *cairn.Job, source enrich.Source, images []cairn.ImageRef) error {
	logger := p.logger.With("link_id", job.ID, "stage", "reading")
	if images == nil {
		detail, err := p.queue.GetBookmark(ctx, job.ID)
		if err != nil {
			return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
		}
		images = detail.Images
		if images == nil {
			images = []cairn.ImageRef{}
		}
	}
	if len(source.ImageURLs) > 0 {
		var err error
		images, err = p.queue.StoreImages(ctx, job.ID, job.LeaseToken, source.ImageURLs)
		if err != nil {
			return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
		}
	}
	result, err := p.stages.reader.Transform(ctx, enrich.Input{ID: job.ID, URL: job.URL, Note: job.Note,
		Attempt: job.Attempt, SourceText: source.OriginalText, RelatedLinks: source.RelatedLinks})
	if err != nil {
		return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
	}
	// Classification is committed only through its own lease; reading aids cannot overwrite it.
	err = p.queue.Complete(ctx, job.ID, cairn.Completion{LeaseToken: job.LeaseToken, AITitle: result.AITitle,
		OriginalLanguage: result.OriginalLanguage, OriginalText: source.OriginalText, TranslatedText: result.TranslatedText,
		Summary: result.Summary, RelatedLinks: source.RelatedLinks, Images: images, Model: result.Model})
	if err != nil {
		return fmt.Errorf("save reading aids: %w", err)
	}
	return nil
}

// RunClassifications drains only semantic jobs and never invokes retrieval.
// A component-level fault (configuration or contract) stops the loop instead of
// burning every queued job's attempt budget; a stale job is not a model failure
// and does not abort the batch.
func (p *Processor) RunClassifications(ctx context.Context, maxJobs int) (int64, int64, error) {
	if p.stages == nil {
		return 0, 0, nil
	}
	s := p.stages
	var completed, failed int64
	for range maxJobs {
		if ctx.Err() != nil {
			break
		}
		job, err := s.queue.ClaimClassification(ctx, s.version, s.model)
		if err != nil {
			if enrich.PausesComponent(err) {
				// The component cannot make progress; surface once and stop rather
				// than treating every queued job as a failure.
				return completed, failed, fmt.Errorf("classification paused: %w", err)
			}
			return completed, failed, fmt.Errorf("claim classification: %w", err)
		}
		if job == nil {
			break
		}
		// An already acquired lease finishes under its own bounded deadline.
		// WithoutCancel keeps shutdown from tearing down a paid inference that is
		// about to succeed, but the deadline stops an unbounded drain.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.classificationDeadline)
		result, err := s.classifier.Classify(workCtx, job.Input)
		if err != nil {
			cancel()
			if enrich.IsStale(err) {
				// Superseded input/target: not a semantic failure, and the Worker
				// already knows. Do not spend an attempt or abort other jobs.
				if reportErr := s.queue.FailClassification(context.WithoutCancel(ctx), job, "superseded: "+boundedError(err)); reportErr != nil && !enrich.IsStale(reportErr) {
					return completed, failed, errors.Join(err, reportErr)
				}
				continue
			}
			if enrich.PausesComponent(err) {
				// Configuration/contract faults are component-level: stop without
				// spending this job's attempt budget so the queue survives the pause.
				// The lease is deliberately left to expire rather than marked failed.
				return completed, failed, fmt.Errorf("classification paused: %w", err)
			}
			failed++
			if reportErr := s.queue.FailClassification(context.WithoutCancel(ctx), job, boundedError(err)); reportErr != nil {
				return completed, failed, errors.Join(err, reportErr)
			}
			p.logger.WarnContext(ctx, "classification failed; source retained", "link_id", job.ID, "error", err)
			continue
		}
		if err := s.queue.CompleteClassification(workCtx, job, result); err != nil {
			cancel()
			// Already-completed means the commit succeeded but the response was
			// lost; that is a success, not a failure, and must not be retried with
			// another paid inference.
			if enrich.ClassOf(err) == enrich.ErrorClassCompleted {
				completed++
				continue
			}
			failed++
			return completed, failed, fmt.Errorf("save classification: %w", err)
		}
		cancel()
		completed++
	}
	return completed, failed, nil
}
