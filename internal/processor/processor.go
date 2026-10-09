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

// ErrJobDeferred means the Worker released a source lease before a paid stage.
// The durable job remains pending and this claim is neither success nor failure.
var ErrJobDeferred = errors.New("source job deferred")

type localStageFault struct {
	stage string
	cause error
}

func (e *localStageFault) Error() string { return e.cause.Error() }
func (e *localStageFault) Unwrap() error { return e.cause }

type sourceStageProbe struct {
	stage   string
	epoch   uint64
	gate    *componentPause
	done    bool
	outcome string
}

func (p *sourceStageProbe) succeed(stage string) {
	if p == nil || p.done || p.stage != stage {
		return
	}
	applied := p.gate.finishStageProbe(p.epoch, true, "")
	p.done = true
	if applied {
		p.outcome = "stage_probe_succeeded"
	} else {
		p.outcome = "stage_probe_superseded"
	}
}

func (p *sourceStageProbe) release() {
	if p == nil || p.done {
		return
	}
	applied := p.gate.releaseStageProbe(p.epoch)
	p.done = true
	if applied {
		p.outcome = "stage_probe_released"
	} else {
		p.outcome = "stage_probe_superseded"
	}
}

func (p *sourceStageProbe) fail(err error) {
	if p == nil || p.done {
		return
	}
	p.gate.finishStageProbe(p.epoch, false, boundedError(err))
	p.done = true
	p.outcome = "stage_probe_failed"
}

// Queue leases work and conditionally stores outcomes.
type Queue interface {
	Claim(context.Context) (*cairn.Job, error)
	GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error)
	StoreImages(context.Context, int64, string, []string) ([]cairn.ImageRef, error)
	Complete(context.Context, int64, cairn.Completion) error
	Fail(context.Context, int64, string, string) error
}

type sourceStageClaimer interface {
	ClaimAllowed(context.Context, bool, bool) (*cairn.Job, error)
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
	stages       *stages
	queue        Queue
	enricher     enrich.Enricher
	logger       *slog.Logger
	concurrency  int
	slots        chan struct{}
	claimTimeout time.Duration
}

// SetClaimTimeout bounds each queue request without imposing a time window on
// the whole round. A slow paid job must not stop future claims merely because
// the previous batch started several seconds earlier.
func (p *Processor) SetClaimTimeout(timeout time.Duration) {
	p.claimTimeout = timeout
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

// ProcessWithSource handles one job using explicit caller-supplied source text.
func (p *Processor) ProcessWithSource(ctx context.Context, job *cairn.Job, sourceText string) error {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.processLeasedJob(ctx, job, sourceText)
}

// processLeasedJob runs while the caller owns one execution slot.
func (p *Processor) processLeasedJob(ctx context.Context, job *cairn.Job, sourceText string) error {
	return p.processLeasedJobWithProbe(ctx, job, sourceText, nil)
}

func (p *Processor) processLeasedJobWithProbe(ctx context.Context, job *cairn.Job,
	sourceText string, probe *sourceStageProbe) error {
	// Already-claimed work may outlive the batch cancellation, but never its
	// lease or a fixed per-job bound. Paid-stage admission separately reserves
	// the commit margin; the final Worker write may use the remaining lease.
	deadline := time.Now().Add(sourceJobMaxDuration)
	if job.LeaseUntil != "" {
		leaseUntil, err := time.Parse(time.RFC3339Nano, job.LeaseUntil)
		if err != nil {
			return enrich.Classified(fmt.Errorf("invalid source lease deadline: %w", err), enrich.ErrorClassContract)
		}
		if time.Until(leaseUntil) <= paidStageCommitMargin {
			return enrich.Classified(errors.New("source lease has too little time remaining"), enrich.ErrorClassStale)
		}
		if leaseUntil.Before(deadline) {
			deadline = leaseUntil
		}
	}
	if !deadline.After(time.Now()) {
		return enrich.Classified(errors.New("source lease has too little time remaining"), enrich.ErrorClassStale)
	}
	workCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return p.processJobWithProbe(workCtx, job, sourceText, probe)
}

// Worker source leases last 15 minutes. This also bounds malformed older
// callers that omit the lease timestamp.
const sourceJobMaxDuration = 15 * time.Minute

// Run processes one bounded source and classification round. The one-shot CLI
// keeps this combined behavior; serve schedules the two queues independently.
//
// Cancelling ctx stops the batch from claiming new work and bounds the run, but
// an already-claimed job is allowed to finish while its own request deadline
// holds. Cancelling the work itself would interrupt jobs mid-request and waste
// their lease, which is exactly what graceful shutdown is trying to avoid.
func (p *Processor) Run(ctx context.Context, maxJobs int) (Stats, error) {
	return p.run(ctx, maxJobs, true)
}

// RunClassificationsOnly drains the semantic queue for a one-shot invocation
// whose source queue was observed empty before startup. It never claims a
// source lease, so it needs neither a Grok credential nor a paid canary.
func (p *Processor) RunClassificationsOnly(ctx context.Context, maxJobs int) (Stats, error) {
	return p.run(ctx, maxJobs, false)
}

func (p *Processor) run(ctx context.Context, maxJobs int, withSource bool) (Stats, error) {
	started := time.Now().UTC()
	if maxJobs < 1 {
		return Stats{StartedAt: started, Duration: time.Since(started)}, nil
	}
	var classificationErr error
	var classified, classificationFailed int64
	classificationDone := make(chan struct{})
	go func() {
		defer close(classificationDone)
		defer RecoverJob(p.logger, "classification batch", 0, &classificationErr)
		classified, classificationFailed, classificationErr = p.RunClassifications(ctx, maxJobs)
	}()
	stats := Stats{StartedAt: started}
	var sourceErr error
	if withSource {
		stats, sourceErr = p.RunSources(ctx, maxJobs)
	}
	<-classificationDone
	stats.Classified, stats.ClassificationFailed = classified, classificationFailed
	// Unlike serve, the one-shot command has no later evidence tick. Recover
	// pending requests before using the remaining classification allowance so
	// a newly appended snapshot can be classified in this invocation.
	if ctx.Err() == nil {
		p.RunEvidenceRecovery(ctx, maxJobs)
	}
	if classificationErr == nil && ctx.Err() == nil && p.stages != nil {
		remaining := maxJobs - int(stats.Classified+stats.ClassificationFailed)
		if remaining > 0 {
			done, failed, err := p.RunClassifications(ctx, remaining)
			stats.Classified += done
			stats.ClassificationFailed += failed
			classificationErr = err
		}
	}
	stats.StartedAt = started
	stats.Duration = time.Since(started)
	return stats, errors.Join(sourceErr, classificationErr)
}

// RunSources claims one bounded source round. It never waits for semantic work,
// so a slow classifier or evidence recovery cannot hold up source retrieval.
func (p *Processor) RunSources(ctx context.Context, maxJobs int) (Stats, error) {
	started := time.Now().UTC()
	stats := Stats{StartedAt: started}
	if maxJobs < 1 {
		stats.Duration = time.Since(started)
		return stats, nil
	}
	if err := p.checkSourcePreflight(ctx); err != nil {
		stats.Duration = time.Since(started)
		return stats, err
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
	var readingSkipLogged atomic.Bool
	var firstErr error
	var errOnce sync.Once
	var workers sync.WaitGroup

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
				// Capacity is acquired before the Worker lease. A manual task or
				// another batch can hold the shared slots without aging a claim.
				finishCapacity := trackProgress(claimCtx, sourceJobMaxDuration+30*time.Second)
				select {
				case p.slots <- struct{}{}:
					finishCapacity()
				case <-claimCtx.Done():
					finishCapacity()
					return
				}
				more := func() bool {
					defer func() { <-p.slots }()
					if claimCtx.Err() != nil {
						return false
					}
					requestCtx := claimCtx
					stopRequest := func() {}
					if p.claimTimeout > 0 {
						requestCtx, stopRequest = context.WithTimeout(claimCtx, p.claimTimeout)
					}
					readingAllowed := true
					var readingHalf bool
					var readingEpoch uint64
					if p.stages != nil {
						// Source retrieval is retired; only reading can acquire work.
						readingAllowed, readingHalf, readingEpoch, _ = p.stages.readingPause.beginStageProbe()
						if !readingAllowed && readingSkipLogged.CompareAndSwap(false, true) {
							p.logger.InfoContext(claimCtx, "source stage event",
								"event_name", "claim_skipped_local_pause", "stage", "reading")
						}
					}
					releaseUnclaimed := func() {
						if readingHalf {
							p.stages.readingPause.releaseStageProbe(readingEpoch)
						}
					}
					if !readingAllowed {
						stopRequest()
						return false
					}
					var job *cairn.Job
					var err error
					finishClaim := trackProgress(claimCtx, p.claimGrace())
					defer finishClaim()
					if staged, ok := p.queue.(sourceStageClaimer); ok {
						job, err = staged.ClaimAllowed(requestCtx, false, readingAllowed)
					} else if readingAllowed && !readingHalf {
						job, err = p.queue.Claim(requestCtx)
					} else {
						err = errors.New("source queue does not support stage-filtered claims")
					}
					stopRequest()
					finishClaim()
					if err != nil {
						releaseUnclaimed()
						if claimCtx.Err() != nil {
							return false
						}
						recordFatal(fmt.Errorf("claim enrichment job: %w", err))
						return false
					}
					if job == nil {
						releaseUnclaimed()
						return false
					}
					finishJob := trackProgress(workCtx, sourceJobMaxDuration+30*time.Second)
					defer finishJob()
					var probe *sourceStageProbe
					if readingHalf {
						if job.SourceComponent == "reading" {
							probe = &sourceStageProbe{stage: "reading", epoch: readingEpoch, gate: p.stages.readingPause}
						} else {
							p.stages.readingPause.releaseStageProbe(readingEpoch)
						}
					}
					if probe != nil {
						p.logger.InfoContext(workCtx, "source stage event",
							"event_name", "stage_probe_started", "stage", probe.stage)
					}
					// The outer worker recovers panics. Unwind this claim's probe first
					// so a recovered worker cannot strand its half-open stage forever.
					defer func() {
						if probe != nil && !probe.done {
							probe.fail(errors.New("source stage probe interrupted"))
						}
						if probe != nil {
							p.logger.InfoContext(workCtx, "source stage event",
								"event_name", probe.outcome, "stage", probe.stage)
						}
					}()
					claimed.Add(1)
					// Already-claimed work may finish after batch cancellation,
					// within its own lease and per-job deadline.
					err = p.processLeasedJobWithProbe(workCtx, job, "", probe)
					if probe != nil && !probe.done {
						if err == nil || errors.Is(err, ErrJobDeferred) || enrich.IsStale(err) ||
							enrich.ClassOf(err) == enrich.ErrorClassBudget {
							probe.release()
						} else {
							probe.fail(err)
						}
					}
					if err != nil {
						if errors.Is(err, ErrJobDeferred) {
							return true
						}
						failed.Add(1)
						var stageFault *localStageFault
						if errors.As(err, &stageFault) {
							return true
						}
						if sourceComponentFailure(err) {
							recordFatal(fmt.Errorf("source component paused: %w", err))
							return false
						}
						// One bad bookmark does not stop unrelated source work.
						return true
					}
					completed.Add(1)
					return true
				}()
				if !more {
					return
				}
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

// Source content errors stay local to one bookmark. A failed Worker write is
// shared infrastructure; continuing to claim would burn every lease while the
// queue is unavailable. Typed stale/completed replies are per-job exceptions.
func sourceComponentFailure(err error) bool {
	if enrich.PausesComponent(err) {
		return true
	}
	var apiErr *cairn.APIError
	return errors.As(err, &apiErr) && !enrich.IsStale(err) && apiErr.Class() != enrich.ErrorClassCompleted
}

func (p *Processor) processJobWithProbe(ctx context.Context, job *cairn.Job,
	sourceText string, probe *sourceStageProbe) error {
	if p.stages != nil {
		return p.processStages(ctx, job, strings.TrimSpace(sourceText), probe)
	}
	logger := p.logger.With("link_id", job.ID, "attempt", job.Attempt)
	logger.InfoContext(ctx, "enrichment started")
	var existing cairn.BookmarkDetail
	useExisting := false
	sourceText = strings.TrimSpace(sourceText)
	if sourceText == "" {
		var detailErr error
		existing, detailErr = p.queue.GetBookmark(ctx, job.ID)
		if detailErr != nil {
			logger.WarnContext(ctx, "failed to inspect existing enrichment detail", "error", detailErr)
		} else if strings.TrimSpace(existing.OriginalText) != "" {
			sourceText = existing.OriginalText
			useExisting = true
			logger.InfoContext(ctx, "recovering partial enrichment from existing source text")
		}
	}

	if sourceText == "" {
		return enrich.ErrCaptureRequired
	}
	path := failurePathRecovered
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

func relatedLinks(resultLinks, existingLinks []string) []string {
	if len(resultLinks) > 0 || len(existingLinks) == 0 {
		return resultLinks
	}
	return existingLinks
}

func (p *Processor) reportFailure(ctx context.Context, logger *slog.Logger, job *cairn.Job, path failurePathLabel, err error) error {
	return p.reportFailureAtStage(ctx, logger, job, path, "", err)
}

func (p *Processor) reportStageFailure(ctx context.Context, logger *slog.Logger, job *cairn.Job, path failurePathLabel, stage string, err error) error {
	return p.reportFailureAtStage(ctx, logger, job, path, stage, err)
}

func (p *Processor) reportFailureAtStage(ctx context.Context, logger *slog.Logger, job *cairn.Job, path failurePathLabel, stage string, err error) error {
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
	var localFault bool
	if stage != "" && p.stages != nil {
		class := enrich.ClassOf(err)
		var apiErr *cairn.APIError
		if (class == enrich.ErrorClassConfiguration || class == enrich.ErrorClassContract) &&
			!errors.As(err, &apiErr) {
			localFault = true
			pause := p.stages.pauseFor(stage)
			if pause.tripStage(boundedError(err)) {
				_, _, remaining := pause.state()
				component := stage
				if component == "fetch" {
					component = "source"
				}
				p.logger.WarnContext(ctx, "source stage event", "event_name", "local_stage_paused",
					"stage", component, "error_class", string(class), "backoff_ms", remaining.Milliseconds())
			}
		}
	}
	var reportErr error
	if stage != "" {
		var providerErr *enrich.ModelHTTPError
		var retryAfter time.Duration
		if errors.As(err, &providerErr) {
			retryAfter = providerErr.RetryAfter
		}
		reportErr = p.stages.queue.FailSourceStage(ctx, job.ID, job.LeaseToken, stage, message,
			retryAfter, enrich.IsRetryable(err))
	} else {
		reportErr = p.queue.Fail(ctx, job.ID, job.LeaseToken, message)
	}
	if reportErr != nil {
		logger.ErrorContext(ctx, "failed to report enrichment failure", "error", reportErr)
		return fmt.Errorf("report enrichment failure: %w", reportErr)
	}
	logger.WarnContext(ctx, "enrichment failed", "error", message)
	if localFault {
		return &localStageFault{stage: stage, cause: err}
	}
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
