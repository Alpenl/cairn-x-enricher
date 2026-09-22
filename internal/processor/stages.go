package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
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
	// GetLatestRun returns the newest recorded run, used only by the opt-in
	// partial re-evaluation.
	GetLatestRun(context.Context, int64) (*cairn.StoredRun, error)
	// GetQuestionSpec loads the immutable spec a stored run was evaluated with.
	GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error)
	// SubmitEntityState records the bounded entity lifecycle result.
	SubmitEntityState(context.Context, int64, map[string]any) error
	// CreateEvidenceRequest records a bounded escalation; the consumer performs
	// the fetch under its own network policy. The status says whether a fetch is
	// still needed.
	CreateEvidenceRequest(context.Context, int64, map[string]any) (cairn.EvidenceRequestAck, error)
	RecoverableEvidenceRequests(context.Context, int) ([]cairn.EvidenceExecution, error)
	ClaimEvidenceRequest(context.Context, string, string) (cairn.EvidenceExecution, error)
	CheckpointEvidenceRequest(context.Context, string, string, any) (string, error)
	FinalizeEvidenceRequest(context.Context, string, string) (cairn.EvidenceReceipt, error)
	// GetEvidenceAt loads the exact snapshot the lease was bound to.
	GetEvidenceAt(context.Context, int64, int64) (json.RawMessage, error)
	// GetEvidence detects a missing checkpoint after a source-only success.
	GetEvidence(context.Context, int64) (json.RawMessage, error)
	// AckSourceRefresh consumes the one-shot refresh intent.
	AckSourceRefresh(context.Context, int64, int64, string, string) error
	// DecideEvidenceRequest reports the bounded outcome of an escalation.
	DecideEvidenceRequest(context.Context, string, map[string]any) error
	// RetryClassification re-arms the classification queue after new evidence.
	RetryClassification(context.Context, int64) error
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
	// extensions is optional. When absent every extension stays off and the
	// pipeline is identical to the default.
	extensions  *extension.Service
	fetcher     *http.Client
	fetchPolicy extension.FetchPolicy
	// partialReuse opts in to reusing unchanged stored answers. It defaults off:
	// the conservative full evaluation is the production default (R2-13).
	partialReuse bool
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
	// probing is set while one half-open probe owns the recovery attempt.
	probing bool
}

const (
	pauseBaseBackoff = 30 * time.Second
	pauseMaxBackoff  = 10 * time.Minute
)

func newComponentPause() *componentPause {
	return &componentPause{now: time.Now}
}

// beginProbe reports whether work may be attempted right now. A healthy
// component always allows work. A paused component only allows one atomic
// half-open probe after its backoff elapsed; concurrent callers are refused so
// a configuration fault cannot be probed by draining the business queue
// (R2-09).
func (p *componentPause) beginProbe() (allowed, halfOpen bool, remaining time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() {
		return true, false, 0
	}
	if remaining := p.until.Sub(p.now()); remaining > 0 {
		return false, false, remaining
	}
	if p.probing {
		// Another caller owns the single probe for this window.
		return false, false, pauseBaseBackoff
	}
	p.probing = true
	return true, true, 0
}

// endProbe records the probe outcome. Only a success clears the breaker; a
// failure extends it with the next backoff step.
func (p *componentPause) endProbe(success bool, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if success {
		p.until = time.Time{}
		p.reason = ""
		p.failures = 0
		p.probing = false
		return
	}
	// Release ownership and extend the backoff under the same lock: a second
	// caller must not acquire the expired window between those two actions.
	p.tripLocked(reason)
}

func (p *componentPause) trip(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tripLocked(reason)
}

func (p *componentPause) tripLocked(reason string) {
	p.probing = false
	p.failures++
	backoff := pauseBaseBackoff << min(p.failures-1, 8)
	if backoff > pauseMaxBackoff || backoff <= 0 {
		backoff = pauseMaxBackoff
	}
	p.until = p.now().Add(backoff)
	p.reason = reason
}

// ClassificationPaused reports the component pause for health reporting.
func (p *Processor) ClassificationPaused() (bool, string, time.Duration) {
	if p.stages == nil || p.stages.pause == nil {
		return false, "", 0
	}
	return p.stages.pause.state()
}

// isHealthy reports whether the breaker is currently closed.
func (p *componentPause) isHealthy() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.until.IsZero()
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

// SetPartialReuse enables the opt-in partial re-evaluation.
func (p *Processor) SetPartialReuse(enabled bool) {
	if p.stages != nil {
		p.stages.partialReuse = enabled
	}
}

// Extensions exposes the attached extension service for the management UI so a
// rerank action uses the same flags, budget and judge as the pipeline.
func (p *Processor) Extensions() *extension.Service {
	if p.stages == nil {
		return nil
	}
	return p.stages.extensions
}

// SetExtensions attaches the bounded semantic extensions. Without one the
// processor behaves exactly as before, so the default stays off.
func (p *Processor) SetExtensions(service *extension.Service, fetcher *http.Client, policy extension.FetchPolicy) {
	if p.stages == nil {
		return
	}
	p.stages.extensions = service
	p.stages.fetcher = fetcher
	p.stages.fetchPolicy = policy
}

func (p *Processor) processStages(ctx context.Context, job *cairn.Job, manual string) error {
	s := p.stages
	logger := p.logger.With("link_id", job.ID, "attempt", job.Attempt)
	input := enrich.Input{ID: job.ID, URL: job.URL, Note: job.Note, Attempt: job.Attempt, SourceText: manual}
	var source *enrich.Source
	var err error
	// An explicit refresh intent bypasses both reuse paths: the operator asked
	// for a real fetch, not for the stored snapshot (R2-06).
	if manual == "" && job.RefreshEpoch > 0 {
		fetched, fetchErr := s.reader.FetchSource(ctx, input)
		if fetchErr != nil {
			// The old readable content and all human data are kept; the intent is
			// consumed so a broken URL cannot loop forever.
			_ = s.queue.AckSourceRefresh(context.WithoutCancel(ctx), job.ID, job.RefreshEpoch, "failed", boundedError(fetchErr))
			return p.reportFailure(ctx, logger, job, failurePathSearch, fetchErr)
		}
		source = &fetched
		if err = p.saveSourceWithEvidence(ctx, job, *source); err != nil {
			_ = s.queue.AckSourceRefresh(context.WithoutCancel(ctx), job.ID, job.RefreshEpoch, "failed", boundedError(err))
			return p.reportFailure(ctx, logger, job, failurePathSearch, err)
		}
		_ = s.queue.AckSourceRefresh(context.WithoutCancel(ctx), job.ID, job.RefreshEpoch, "completed", "")
		logger.InfoContext(ctx, "source refreshed; classification queued")
		return p.finishReading(ctx, job, *source, nil)
	}
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
	} else if err = p.ensureSourceEvidence(ctx, job.ID, *source); err != nil {
		return p.reportFailure(ctx, logger, job, failurePathRecovered, err)
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
	return p.persistSourceEvidence(ctx, job.ID, source)
}

// A source checkpoint can succeed before its snapshot write fails. Retrying
// reading repairs that checkpoint without fetching again or replacing an
// existing current snapshot (which may already contain escalated evidence).
func (p *Processor) ensureSourceEvidence(ctx context.Context, id int64, source enrich.Source) error {
	payload, err := p.stages.queue.GetEvidence(ctx, id)
	if err != nil && !cairn.IsUnsupported(err) {
		return fmt.Errorf("check evidence checkpoint: %w", err)
	}
	if err == nil && len(payload) > 0 {
		var view struct {
			Current bool `json:"current"`
		}
		if err := json.Unmarshal(payload, &view); err != nil {
			return fmt.Errorf("decode evidence checkpoint: %w", err)
		}
		if view.Current {
			return nil
		}
	}
	return p.persistSourceEvidence(ctx, id, source)
}

func (p *Processor) persistSourceEvidence(ctx context.Context, id int64, source enrich.Source) error {
	snapshot := EvidenceSnapshot(source, time.Now())
	if err := p.stages.queue.SubmitEvidence(ctx, id, snapshot); err != nil {
		if cairn.IsUnsupported(err) {
			p.logger.InfoContext(ctx, "evidence snapshots unsupported by backend; continuing in v1 mode", "link_id", id)
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

// RunClassifications drains semantic jobs without retrieving the primary source.
// Explicitly enabled evidence extensions can fetch one bounded external gap
// under durable ownership and recover a stored outcome before claiming work.
// A component-level fault (configuration or contract) stops the loop instead of
// burning every queued job's attempt budget; a stale job is not a model failure
// and does not abort the batch.
func (p *Processor) RunClassifications(ctx context.Context, maxJobs int) (int64, int64, error) {
	if p.stages == nil || maxJobs <= 0 {
		return 0, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	s := p.stages
	evidenceRemaining := 0
	if s.extensions != nil && s.extensions.Flags.Evidence && s.fetcher != nil {
		evidenceRemaining = min(maxJobs, 20, s.extensions.Budget.MaxCallsTotal)
		p.recoverEvidence(ctx, &evidenceRemaining)
	}
	var completed, failed int64
	// The pause gate runs before any claim. A component that failed on the
	// previous poll must not acquire another lease until the backoff elapses:
	// claiming already increments the Worker's attempt counter (F09). The
	// recovery attempt is a single atomic half-open probe (R2-09).
	allowed, probePending, remaining := s.pause.beginProbe()
	if !allowed {
		_, reason, _ := s.pause.state()
		return completed, failed, fmt.Errorf("%w: %s (probe in %s)", ErrComponentPaused, reason, remaining.Round(time.Second))
	}
	if probePending {
		// One half-open owner may test one leased job, not drain a batch while
		// its provider is still failing. Every unsettled return releases it.
		maxJobs = 1
	}
	defer func() {
		if probePending {
			_, reason, _ := s.pause.state()
			s.pause.endProbe(false, reason)
		}
	}()
	settleProbe := func(success bool, reason string) {
		if probePending {
			s.pause.endProbe(success, reason)
			probePending = false
			return
		}
		if !success {
			s.pause.trip(reason)
		}
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
				settleProbe(false, err.Error())
				return completed, failed, fmt.Errorf("%w: %w", ErrComponentPaused, err)
			}
			return completed, failed, fmt.Errorf("claim classification: %w", err)
		}
		// A reachable Worker says nothing about the provider configuration: a
		// claim success must not clear a model-side breaker (R2-09).
		if job == nil {
			// Empty only proves Worker reachability. The deferred settlement
			// preserves the provider failure and schedules another bounded probe.
			break
		}
		// The evaluation consumes exactly the snapshot the lease was bound to,
		// preserving every stored block and role (R2-07).
		// An already acquired lease finishes under its own bounded deadline.
		// WithoutCancel keeps shutdown from tearing down a paid inference that is
		// about to succeed, but the deadline stops an unbounded drain.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.classificationDeadline)
		var result classify.Result
		err = p.attachBoundEvidence(workCtx, job)
		if err == nil {
			result, err = p.classifyJob(workCtx, job)
		}
		if err != nil {
			cancel()
			if len(result.RawJudgments.Calls) > 0 {
				p.logger.WarnContext(ctx, "classification inference attempt failed; no inference fallback", "link_id", job.ID, "provider_calls", result.RawJudgments.Calls)
			}
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
				settleProbe(false, err.Error())
				return completed, failed, fmt.Errorf("%w: %w", ErrComponentPaused, err)
			}
			failed++
			if reportErr := s.queue.FailClassification(context.WithoutCancel(ctx), job, boundedError(err)); reportErr != nil {
				return completed, failed, errors.Join(err, reportErr)
			}
			p.logger.WarnContext(ctx, "classification failed; source retained", "link_id", job.ID, "error", err)
			continue
		}
		// The model stage succeeded: only now may the breaker clear.
		settleProbe(true, "")
		if err := s.queue.CompleteClassification(workCtx, job, result); err != nil {
			cancel()
			// The client confirms lost responses by replaying this exact operation.
			// Generic already_completed can refer to unrelated work and is not an
			// acknowledgment; never run extensions after an unconfirmed commit.
			failed++
			return completed, failed, fmt.Errorf("save classification: %w", err)
		}
		cancel()
		completed++
		p.runExtensions(ctx, job, &evidenceRemaining)
	}
	return completed, failed, nil
}

// classifyJob evaluates one job, optionally reusing stored answers when the
// opt-in partial re-evaluation is enabled and the previous run is compatible.
// Any failure to reconstruct the previous run falls back to the full
// evaluation; it never fails the job (R2-13).
func (p *Processor) classifyJob(ctx context.Context, job *cairn.ClassificationJob) (classify.Result, error) {
	s := p.stages
	if s.partialReuse {
		reuser, ok := s.classifier.(interface {
			ClassifyReusing(context.Context, classify.Input, *classify.RawJudgments, string) (classify.Result, error)
		})
		if ok {
			if previous := p.previousJudgments(ctx, job); previous != nil {
				return reuser.ClassifyReusing(ctx, job.Input, previous, "single-request")
			}
		}
	}
	return s.classifier.Classify(ctx, job.Input)
}

// previousJudgments reconstructs the replayable judgments of the newest stored
// run. A missing spec, a decode failure or a partial run returns nil so the
// caller falls back to a full evaluation.
func (p *Processor) previousJudgments(ctx context.Context, job *cairn.ClassificationJob) *classify.RawJudgments {
	s := p.stages
	run, err := s.queue.GetLatestRun(ctx, job.ID)
	if err != nil || run == nil || run.Status != "succeeded" || run.Coverage != "complete" {
		return nil
	}
	stored, err := s.queue.GetQuestionSpec(ctx, run.SpecID)
	if err != nil {
		return nil
	}
	spec, err := classify.DecodeSpec(stored.Payload)
	if err != nil {
		return nil
	}
	raw, err := run.DecodeJudgments(spec)
	if err != nil || raw.MetadataVersion != 1 || raw.EvidenceHash == "" || raw.BatchSemantics == "" {
		return nil
	}
	return &raw
}

// runExtensions runs the opt-in bounded extensions after a successful
// classification. Every failure is logged and swallowed: an extension can never
// fail the bookmark or lose the classification that was already committed.
func (p *Processor) runExtensions(ctx context.Context, job *cairn.ClassificationJob, remaining *int) {
	s := p.stages
	if s.extensions == nil {
		return
	}
	var archived struct {
		Blocks []extension.Block `json:"blocks"`
	}
	if len(job.BoundSnapshot) == 0 || json.Unmarshal(job.BoundSnapshot, &archived) != nil || len(archived.Blocks) == 0 {
		p.logger.WarnContext(ctx, "extensions require verified bound evidence", "link_id", job.ID)
		return
	}
	blocks := archived.Blocks
	// Entities are additive and independently budgeted. A stale or failed run
	// is recorded explicitly and never clears a newer success.
	entity := s.extensions.Entities(ctx, blocks, job.RelatedLinks)
	if entity.State != extension.EntityNotRun {
		body := map[string]any{
			"operation_key":        fmt.Sprintf("entity-%d-rev-%d-lease-%s", job.ID, job.Revision, job.LeaseToken),
			"state":                string(entity.State),
			"entities":             entity.Entities,
			"content_revision":     job.ContentRevision,
			"content_hash":         job.EvidenceHash,
			"evidence_snapshot_id": job.EvidenceSnapshotID,
			"reason":               entity.Reason,
			"calls":                entity.Calls,
			"tokens":               entity.Tokens,
		}
		if err := s.queue.SubmitEntityState(context.WithoutCancel(ctx), job.ID, body); err != nil {
			p.logger.WarnContext(ctx, "entity state was not stored", "link_id", job.ID, "error", err)
		}
	}
	if !s.extensions.Flags.Evidence || s.fetcher == nil || *remaining <= 0 {
		return
	}
	rawURL := firstMissingAllowlisted(blocks, job.RelatedLinks, s.fetchPolicy)
	if rawURL == "" {
		return
	}
	createCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var ack cairn.EvidenceRequestAck
	err := retryEvidenceWrite(createCtx, func() error {
		var err error
		ack, err = s.queue.CreateEvidenceRequest(createCtx, job.ID, map[string]any{
			"protocol": 1, "scope": "external_link", "dedupe_key": fmt.Sprintf("evidence-%d-%d-%s", job.ID, job.EvidenceSnapshotID, shortURLHash(rawURL)),
			"evidence_snapshot_id": job.EvidenceSnapshotID, "source_hash": job.EvidenceHash, "content_revision": job.ContentRevision,
			"target_generation": job.TargetGeneration, "url": rawURL,
			"budget": map[string]any{"max_bytes": s.fetchPolicy.MaxBytes, "timeout_ms": s.fetchPolicy.Timeout.Milliseconds()},
		})
		return err
	})
	if err != nil {
		p.logger.WarnContext(ctx, "evidence request was not stored", "link_id", job.ID, "error", err)
		return
	}
	// Replayed pending intents also go through the atomic claim; replay is never
	// interpreted as ownership. Checkpointed results can be finalized without fetch.
	p.executeEvidence(ctx, ack.ID, remaining)
}

// attachBoundEvidence loads the snapshot the lease was bound to and builds the
// structured provider state from it. Bound identity failures stop evaluation;
// only an explicitly unbound legacy lease may use the plain text fields.
func (p *Processor) attachBoundEvidence(ctx context.Context, job *cairn.ClassificationJob) error {
	if job.EvidenceSnapshotID < 1 {
		if job.TargetGeneration > 0 || job.EvidenceHash != "" {
			return enrich.Classified(errors.New("classification lease has no bound evidence"), enrich.ErrorClassContract)
		}
		return nil
	}
	payload, err := p.stages.queue.GetEvidenceAt(ctx, job.ID, job.EvidenceSnapshotID)
	if err != nil {
		return fmt.Errorf("load bound evidence: %w", err)
	}
	var identity struct {
		ID              int64           `json:"id"`
		ContentRevision int64           `json:"content_revision"`
		ContentHash     string          `json:"content_hash"`
		Snapshot        json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(payload, &identity); err != nil {
		return enrich.Classified(fmt.Errorf("decode bound evidence: %w", err), enrich.ErrorClassContract)
	}
	if identity.ID != job.EvidenceSnapshotID || identity.ContentRevision != job.ContentRevision || identity.ContentHash != job.EvidenceHash {
		return enrich.Classified(errors.New("bound evidence identity mismatch"), enrich.ErrorClassContract)
	}
	// The Worker returns the canonical objective payload persisted in the
	// snapshot. Check its bytes too, so a response cannot echo the leased hash
	// while supplying different material. Whitespace is not part of identity.
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, identity.Snapshot); err != nil {
		return enrich.Classified(fmt.Errorf("decode snapshot payload: %w", err), enrich.ErrorClassContract)
	}
	if fmt.Sprintf("%x", sha256.Sum256(canonical.Bytes())) != job.EvidenceHash {
		return enrich.Classified(errors.New("bound evidence content hash mismatch"), enrich.ErrorClassContract)
	}
	evidence, err := evidenceFromSnapshot(payload)
	if err != nil {
		return enrich.Classified(fmt.Errorf("bound evidence is unusable: %w", err), enrich.ErrorClassContract)
	}
	job.Evidence = evidence
	job.BoundSnapshot = append(json.RawMessage(nil), identity.Snapshot...)
	return nil
}

// evidenceFromSnapshot converts the stored snapshot into the objective evidence
// the provider sees. Roles, block order and truncation facts are preserved.
func evidenceFromSnapshot(payload json.RawMessage) (*classify.Evidence, error) {
	var view struct {
		Snapshot struct {
			Blocks []struct {
				ID   string `json:"id"`
				Role string `json:"role"`
				Text string `json:"text"`
				URL  string `json:"url"`
			} `json:"blocks"`
			Truncation struct {
				Truncated bool `json:"truncated"`
			} `json:"truncation"`
		} `json:"snapshot"`
		Completeness string `json:"completeness"`
	}
	if err := json.Unmarshal(payload, &view); err != nil {
		return nil, err
	}
	if len(view.Snapshot.Blocks) == 0 {
		return nil, errors.New("snapshot has no blocks")
	}
	evidence := &classify.Evidence{Truncated: view.Snapshot.Truncation.Truncated, Coverage: "complete"}
	if evidence.Truncated {
		evidence.Coverage = "truncated"
	}
	seen := make(map[string]bool, len(view.Snapshot.Blocks))
	for _, block := range view.Snapshot.Blocks {
		if block.ID == "" || seen[block.ID] || strings.TrimSpace(block.Text) == "" {
			return nil, errors.New("snapshot has an invalid or duplicate block")
		}
		seen[block.ID] = true
		role := classify.BlockRole(block.Role)
		switch role {
		case classify.RolePrimary, classify.RoleAuthorContinuation, classify.RoleQuoted, classify.RoleExternalArticle, classify.RoleThirdParty, classify.RoleLegacyUnknown:
		default:
			return nil, errors.New("snapshot has an invalid block role")
		}
		if role == classify.RolePrimary {
			if evidence.Primary != "" {
				return nil, errors.New("snapshot has multiple primary blocks")
			}
			evidence.Primary = block.Text
			continue
		}
		evidence.Context = append(evidence.Context, classify.EvidenceBlock{
			ID: block.ID, Role: role, Text: block.Text, URL: block.URL,
		})
	}
	if strings.TrimSpace(evidence.Primary) == "" {
		return nil, errors.New("snapshot has no primary block")
	}
	return evidence, nil
}
