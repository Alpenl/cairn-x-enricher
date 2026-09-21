package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error)
	CompleteClassification(context.Context, *cairn.ClassificationJob, classify.Result) error
	FailClassification(context.Context, *cairn.ClassificationJob, string) error
	// SubmitEvidence persists the immutable evidence snapshot a run references.
	// A backend without the v2 API reports cairn.IsUnsupported.
	SubmitEvidence(context.Context, int64, any) error
}

// SourceReader separates retrieval from generation of reading aids.
type SourceReader interface {
	FetchSource(context.Context, enrich.Input) (enrich.Source, error)
	Transform(context.Context, enrich.Input) (enrich.Result, error)
}

// Classifier judges stored evidence without retrieving or modifying it. SpecID
// is part of the interface because the claim must announce the exact immutable
// question set this build compiled, not a hard-coded version.
type Classifier interface {
	Classify(context.Context, classify.Input) (classify.Result, error)
	SpecID() string
}

type stages struct {
	queue                  StageQueue
	reader                 SourceReader
	classifier             Classifier
	version, model         string
	classificationDeadline time.Duration
	pause                  *componentPause
}

// ErrComponentPaused reports that the classification component is in a
// deliberate, recoverable pause. It is distinct from a job failure: no new
// lease is acquired while paused, so the queue's attempt budget survives the
// configuration fault (F09).
var ErrComponentPaused = errors.New("classification component is paused")

// componentPause is the cross-poll circuit breaker. A single 401 or contract
// error pauses the component for a bounded backoff instead of letting the next
// scheduler tick claim another job and burn its attempt. Recovery is a probe:
// once the backoff elapses, exactly one job may be attempted; success clears
// the pause, failure extends it.
type componentPause struct {
	mu       sync.Mutex
	now      func() time.Time
	until    time.Time
	reason   string
	failures int
}

const (
	pauseBaseBackoff = 30 * time.Second
	pauseMaxBackoff  = 10 * time.Minute
)

func newComponentPause() *componentPause {
	return &componentPause{now: time.Now}
}

// allow reports whether a claim may be attempted right now. When paused it also
// reports the remaining backoff so the caller can log it.
func (p *componentPause) allow() (bool, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() {
		return true, 0
	}
	if remaining := p.until.Sub(p.now()); remaining > 0 {
		return false, remaining
	}
	// The backoff elapsed: this call is the single recovery probe.
	return true, 0
}

func (p *componentPause) trip(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures++
	backoff := pauseBaseBackoff << min(p.failures-1, 8)
	if backoff > pauseMaxBackoff || backoff <= 0 {
		backoff = pauseMaxBackoff
	}
	p.until = p.now().Add(backoff)
	p.reason = reason
}

func (p *componentPause) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.until = time.Time{}
	p.reason = ""
	p.failures = 0
}

// ClassificationPaused reports the component pause for health reporting.
func (p *Processor) ClassificationPaused() (bool, string, time.Duration) {
	if p.stages == nil || p.stages.pause == nil {
		return false, "", 0
	}
	return p.stages.pause.state()
}

// state reports the current pause for health reporting.
func (p *componentPause) state() (bool, string, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() {
		return false, "", 0
	}
	remaining := p.until.Sub(p.now())
	if remaining < 0 {
		remaining = 0
	}
	return true, p.reason, remaining
}

// DefaultClassificationDeadline bounds one already-leased classification so a
// graceful shutdown cannot wait forever on a detached work context.
const DefaultClassificationDeadline = 3 * time.Minute

// NewStaged creates the production processor with independent semantic work.
func NewStaged(queue StageQueue, reader SourceReader, classifier Classifier, version, model string, logger *slog.Logger, concurrency int) *Processor {
	p := New(queue, nil, logger, concurrency)
	p.stages = &stages{queue: queue, reader: reader, classifier: classifier, version: version, model: model,
		classificationDeadline: DefaultClassificationDeadline, pause: newComponentPause()}
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
				if err = p.saveSourceWithEvidence(ctx, job, *source); err != nil {
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
		if err = p.saveSourceWithEvidence(ctx, job, *source); err != nil {
			return p.reportFailure(ctx, logger, job, failurePathSearch, err)
		}
		logger.InfoContext(ctx, "source saved; classification queued")
	}
	return p.finishReading(ctx, job, *source, nil)
}

// saveSourceWithEvidence persists the source and then its immutable objective
// snapshot. The snapshot is what a stored run references, so it must exist
// before classification can produce one; a backend without the v2 API is
// tolerated as an explicit legacy fallback (F05).
func (p *Processor) saveSourceWithEvidence(ctx context.Context, job *cairn.Job, source enrich.Source) error {
	s := p.stages
	if err := s.queue.SaveSource(ctx, job.ID, job.LeaseToken, source); err != nil {
		return err
	}
	snapshot := EvidenceSnapshot(source, time.Now())
	if err := s.queue.SubmitEvidence(ctx, job.ID, snapshot); err != nil {
		if cairn.IsUnsupported(err) {
			p.logger.InfoContext(ctx, "evidence snapshots unsupported by backend; continuing in v1 mode", "link_id", job.ID)
			return nil
		}
		return fmt.Errorf("persist evidence snapshot: %w", err)
	}
	return nil
}

// EvidenceSnapshot converts a retrieved source into the immutable objective
// snapshot the v2 domain stores. The note and other personal fields are absent
// by construction, and the provenance of the stored context is honestly
// labelled legacy_unknown rather than guessed (F05).
func EvidenceSnapshot(source enrich.Source, now time.Time) map[string]any {
	blocks := []map[string]any{{
		"id": "primary-1", "role": "primary", "text": source.OriginalText, "acquired": "fetch",
	}}
	if strings.TrimSpace(source.ContextText) != "" {
		blocks = append(blocks, map[string]any{
			"id": "context-1", "role": "legacy_unknown", "text": source.ContextText, "relation": "stored context",
		})
	}
	return map[string]any{
		"blocks":     blocks,
		"fetched_at": now.UTC().Format(time.RFC3339),
		"retrieval":  "x_search",
		"truncation": map[string]any{"truncated": false},
	}
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
	// The pause gate runs before any claim. A component that failed on the
	// previous poll must not acquire another lease until the backoff elapses:
	// claiming already increments the Worker's attempt counter (F09).
	if allowed, remaining := s.pause.allow(); !allowed {
		_, reason, _ := s.pause.state()
		return completed, failed, fmt.Errorf("%w: %s (probe in %s)", ErrComponentPaused, reason, remaining.Round(time.Second))
	}
	for range maxJobs {
		if ctx.Err() != nil {
			break
		}
		job, err := s.queue.ClaimClassification(ctx, s.classifier.SpecID(), s.version, s.model)
		if err != nil {
			if enrich.PausesComponent(err) {
				// Trip the breaker so the next poll does not claim and burn
				// another attempt, then surface the state once.
				s.pause.trip(err.Error())
				return completed, failed, fmt.Errorf("%w: %w", ErrComponentPaused, err)
			}
			return completed, failed, fmt.Errorf("claim classification: %w", err)
		}
		// A reachable claim (even an empty queue) proves the component can talk
		// to the Worker again.
		s.pause.clear()
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
				// Configuration/contract faults are component-level: trip the
				// breaker so the next poll does not claim another job. The
				// current lease is deliberately left to expire rather than marked
				// failed, so this pause costs one attempt, not one per job.
				s.pause.trip(err.Error())
				return completed, failed, fmt.Errorf("%w: %w", ErrComponentPaused, err)
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
