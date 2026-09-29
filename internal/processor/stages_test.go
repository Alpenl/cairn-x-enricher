package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type stageQueue struct {
	*fakeQueue
	mu                       sync.Mutex
	jobPool                  int
	source                   *enrich.Source
	job                      *cairn.ClassificationJob
	classificationFailures   int
	classificationRetryHints []time.Duration
	classified               int
	claims                   int
	claimErr                 error
	completeErr              error
	evidence                 int
	evidenceErr              error
	entityState              map[string]any
	entitySubmissions        int
	evidenceRequests         int
	evidenceDecisions        []map[string]any
	retries                  int
	latestRun                *cairn.StoredRun
	storedSpec               cairn.StoredQuestionSpec
	evidenceSnapshot         json.RawMessage
	evidenceReadErr          error
	evidenceRequestStatus    string
	recoverEvidenceHook      func(context.Context) ([]cairn.EvidenceExecution, error)
	refreshAcks              int
	refreshAckErr            error
	admitCalls               int
	admitErr                 error
	admitErrAt               int
	deferredStages           []string
	deferContextErr          error
}

func (q *stageQueue) AdmitSourceStage(context.Context, int64, string, string, time.Duration) error {
	q.admitCalls++
	if q.admitErrAt == 0 || q.admitCalls == q.admitErrAt {
		return q.admitErr
	}
	return nil
}
func (q *stageQueue) DeferSourceBudget(ctx context.Context, _ int64, _ string, stage string) error {
	q.deferredStages = append(q.deferredStages, stage)
	q.deferContextErr = ctx.Err()
	return nil
}

func (q *stageQueue) GetSource(context.Context, int64) (*enrich.Source, error) { return q.source, nil }
func (q *stageQueue) SaveSource(_ context.Context, id int64, _ string, s enrich.Source) error {
	q.source = &s
	q.job = &cairn.ClassificationJob{ID: id, Input: classify.Input{OriginalText: s.OriginalText}}
	return nil
}
func (q *stageQueue) ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error) {
	// The double is concurrency-safe: concurrent probe tests rely on exactly one
	// caller observing a job.
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if q.jobPool > 0 {
		q.jobPool--
		return &cairn.ClassificationJob{ID: int64(q.claims)}, nil
	}
	j := q.job
	q.job = nil
	return j, nil
}
func (q *stageQueue) CompleteClassification(context.Context, *cairn.ClassificationJob, classify.Result) error {
	if q.completeErr != nil {
		return q.completeErr
	}
	q.classified++
	return nil
}
func (q *stageQueue) FailClassification(_ context.Context, _ *cairn.ClassificationJob, _ string, retryAfter time.Duration, _ bool) error {
	q.classificationFailures++
	q.classificationRetryHints = append(q.classificationRetryHints, retryAfter)
	return nil
}
func (q *stageQueue) SubmitEvidence(context.Context, int64, any) error {
	q.evidence++
	return q.evidenceErr
}
func (q *stageQueue) SubmitEntityState(_ context.Context, _ int64, body map[string]any) error {
	q.entityState = body
	q.entitySubmissions++
	return nil
}
func (q *stageQueue) CreateEvidenceRequest(context.Context, int64, map[string]any) (cairn.EvidenceRequestAck, error) {
	q.evidenceRequests++
	status := q.evidenceRequestStatus
	if status == "" {
		status = "pending"
	}
	return cairn.EvidenceRequestAck{ID: "req-1", Status: status, Replayed: status != "pending"}, nil
}
func (q *stageQueue) RecoverableEvidenceRequests(ctx context.Context, _ int) ([]cairn.EvidenceExecution, error) {
	if q.recoverEvidenceHook != nil {
		return q.recoverEvidenceHook(ctx)
	}
	if q.evidenceRequests > 0 && (q.evidenceRequestStatus == "" || q.evidenceRequestStatus == "pending") {
		return []cairn.EvidenceExecution{{ID: "req-1"}}, nil
	}
	return nil, nil
}

func TestSlowEvidenceRecoveryCannotBlockHalfOpenClassificationProbe(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	q.recoverEvidenceHook = func(ctx context.Context) ([]cairn.EvidenceExecution, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	flags := extension.DefaultFlags()
	flags.Evidence = true
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.95}), &http.Client{}, extension.DefaultFetchPolicy([]string{"allowed.example"}))
	p.stages.pause.trip("previous provider fault")
	p.stages.pause.now = func() time.Time { return time.Now().Add(time.Hour) }
	recoveryDone := make(chan struct{})
	go func() { defer close(recoveryDone); p.RunEvidenceRecovery(context.Background(), 20) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("evidence recovery did not start")
	}
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		done, failed, err := p.RunClassifications(context.Background(), 20)
		if err != nil || done != 1 || failed != 0 {
			t.Errorf("half-open probe did not complete: done=%d failed=%d err=%v", done, failed, err)
		}
	}()
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("slow evidence recovery blocked classification probe")
	}
	close(release)
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("evidence recovery did not finish")
	}
	if paused, _, _ := p.ClassificationPaused(); paused {
		t.Fatal("successful probe did not clear the circuit breaker")
	}
}
func (q *stageQueue) ClaimEvidenceRequest(_ context.Context, id, owner string) (cairn.EvidenceExecution, error) {
	status := q.evidenceRequestStatus
	if status != "" && status != "pending" {
		return cairn.EvidenceExecution{ID: id, Status: status}, nil
	}
	return cairn.EvidenceExecution{ID: id, Status: "fetching", Scope: "external_link", Owned: true, OwnerToken: &owner, URL: "https://allowed.example/article", Budget: cairn.EvidenceBudget{MaxBytes: 2 << 20, TimeoutMS: 15000}}, nil
}
func (q *stageQueue) CheckpointEvidenceRequest(_ context.Context, _ string, _ string, value any) (string, error) {
	outcome := value.(extension.FetchOutcome)
	q.evidenceDecisions = append(q.evidenceDecisions, map[string]any{"status": outcome.State})
	return "fixture-checkpoint", nil
}
func (q *stageQueue) FinalizeEvidenceRequest(context.Context, string, string) (cairn.EvidenceReceipt, error) {
	// The real Worker integration, not this stage double, proves atomicity.
	status := q.evidenceDecisions[len(q.evidenceDecisions)-1]["status"].(string)
	if status == "completed" {
		q.evidence++
		q.retries++
	}
	return cairn.EvidenceReceipt{Status: status, Changed: status == "completed", Requeued: status == "completed"}, nil
}

type evidenceRequeueQueue struct {
	*stageQueue
	requeued *cairn.ClassificationJob
}

func (q *evidenceRequeueQueue) FinalizeEvidenceRequest(ctx context.Context, id, hash string) (cairn.EvidenceReceipt, error) {
	receipt, err := q.stageQueue.FinalizeEvidenceRequest(ctx, id, hash)
	if err == nil && receipt.Requeued {
		q.mu.Lock()
		q.job = q.requeued
		q.mu.Unlock()
	}
	return receipt, err
}

func TestOneShotClassifiesEvidenceRecoveredWithinJobLimit(t *testing.T) {
	job := &cairn.ClassificationJob{ID: 1, Revision: 2,
		Input: classify.Input{URL: "https://x.com/a/status/1", OriginalText: "Acme builds Widgets."}}
	base := &stageQueue{fakeQueue: newFakeQueue(), evidenceRequests: 1}
	bindExtensionFixture(t, base, job)
	queue := &evidenceRequeueQueue{stageQueue: base, requeued: job}
	p := NewStaged(queue, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	flags := extension.DefaultFlags()
	flags.Evidence = true
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.95}),
		&http.Client{Transport: staticTransport{body: "<html><body>Fetched external article body.</body></html>"}},
		extension.DefaultFetchPolicy([]string{"allowed.example"}))

	stats, err := p.Run(context.Background(), 1)
	if err != nil || stats.Classified != 1 || queue.classified != 1 || queue.retries != 1 {
		t.Fatalf("one-shot recovery and classification: stats=%+v classified=%d retries=%d err=%v", stats, queue.classified, queue.retries, err)
	}
}
func (q *stageQueue) GetEvidenceAt(context.Context, int64, int64) (json.RawMessage, error) {
	return q.evidenceSnapshot, q.evidenceReadErr
}
func (q *stageQueue) GetEvidence(context.Context, int64) (json.RawMessage, error) {
	return q.evidenceSnapshot, q.evidenceReadErr
}
func (q *stageQueue) AckSourceRefresh(context.Context, int64, int64, string, string) error {
	q.refreshAcks++
	return q.refreshAckErr
}

func TestDetachedStateReportHasDeadlineAfterBatchCancellation(t *testing.T) {
	batch, stopBatch := context.WithCancel(context.Background())
	stopBatch()
	report, stopReport := boundedStateReportContext(batch)
	defer stopReport()
	deadline, ok := report.Deadline()
	if !ok || report.Err() != nil || time.Until(deadline) <= 0 || time.Until(deadline) > stateReportTimeout {
		t.Fatalf("detached report has no usable bound: deadline=%s present=%t error=%v", deadline, ok, report.Err())
	}
}
func (q *stageQueue) DecideEvidenceRequest(_ context.Context, _ string, body map[string]any) error {
	q.evidenceDecisions = append(q.evidenceDecisions, body)
	return nil
}
func (q *stageQueue) RetryClassification(context.Context, int64) error {
	q.retries++
	return nil
}
func (q *stageQueue) GetLatestRun(context.Context, int64) (*cairn.StoredRun, error) {
	return q.latestRun, nil
}
func (q *stageQueue) GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error) {
	return q.storedSpec, nil
}

type stageReader struct {
	fetches         int
	transforms      int
	fail            bool
	fetchErr        error
	readingLanguage string
	q               *stageQueue
}

func (r *stageReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	if r.fetchErr != nil {
		return enrich.Source{}, r.fetchErr
	}
	return enrich.Source{OriginalText: "saved original", Model: "grok", RelatedLinks: []string{}, ImageURLs: []string{}}, nil
}
func (r *stageReader) Transform(_ context.Context, i enrich.Input) (enrich.Result, error) {
	r.transforms++
	if r.q.source == nil {
		return enrich.Result{}, errors.New("reading started before source persisted")
	}
	if r.fail {
		return enrich.Result{}, errors.New("reading unavailable")
	}
	return enrich.Result{OriginalText: i.SourceText, OriginalLanguage: r.readingLanguage, Summary: "summary"}, nil
}

func TestReadingCompletionPreservesSourceTextAndKnownLanguage(t *testing.T) {
	source := &enrich.Source{OriginalText: " \nsource with significant edges\n ", OriginalLanguage: "en",
		RelatedLinks: []string{"https://example.com/related"},
		ImageURLs:    []string{"https://pbs.twimg.com/media/source-image"}, Model: "source"}
	q := &stageQueue{fakeQueue: newFakeQueue(), source: source}
	r := &stageReader{q: q, readingLanguage: "fr"}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease"}
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got := q.completions[job.ID]
	if got.OriginalText != source.OriginalText || got.OriginalLanguage != "en" ||
		len(got.RelatedLinks) != 1 || got.RelatedLinks[0] != source.RelatedLinks[0] ||
		len(got.Images) != 1 || len(q.imageURLs[job.ID]) != 1 || q.imageURLs[job.ID][0] != source.ImageURLs[0] {
		t.Fatalf("reading completion lost authoritative source fields: %+v", got)
	}
}

type budgetStageReader struct {
	*stageReader
	deniedStage string
	cancel      context.CancelFunc
}

func (r *budgetStageReader) FetchSource(ctx context.Context, input enrich.Input) (enrich.Source, error) {
	if r.deniedStage == "fetch" {
		if r.cancel != nil {
			r.cancel()
		}
		return enrich.Source{}, &cairn.APIError{StatusCode: http.StatusTooManyRequests, Code: "budget_exhausted"}
	}
	return r.stageReader.FetchSource(ctx, input)
}
func (r *budgetStageReader) Transform(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	if r.deniedStage == "reading" {
		if r.cancel != nil {
			r.cancel()
		}
		return enrich.Result{}, &cairn.APIError{StatusCode: http.StatusTooManyRequests, Code: "budget_exhausted"}
	}
	return r.stageReader.Transform(ctx, input)
}

func TestPaidBudgetDenialDefersWithoutReportingModelFailure(t *testing.T) {
	for _, stage := range []string{"fetch", "reading"} {
		t.Run(stage, func(t *testing.T) {
			q := &stageQueue{fakeQueue: newFakeQueue()}
			r := &budgetStageReader{stageReader: &stageReader{q: q}, deniedStage: stage}
			p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
			job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", LeaseToken: "lease", Attempt: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.cancel = cancel
			if err := p.Process(ctx, job); !errors.Is(err, ErrJobDeferred) {
				t.Fatalf("budget denial = %v, want deferred", err)
			}
			if len(q.failures) != 0 || len(q.deferredStages) != 1 || q.deferredStages[0] != stage || q.deferContextErr != nil {
				t.Fatalf("failures=%v deferred=%v context=%v", q.failures, q.deferredStages, q.deferContextErr)
			}
		})
	}
}

func TestManualSourceDoesNotAdmitAFreeFetchAsPaid(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	r := &stageReader{q: q}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", LeaseToken: "lease", Attempt: 1}
	if err := p.ProcessWithSource(context.Background(), job, "pasted original"); err != nil {
		t.Fatal(err)
	}
	if q.admitCalls != 1 || r.fetches != 1 || r.transforms != 1 || len(q.completions) != 1 {
		t.Fatalf("admissions=%d fetches=%d reading=%d completions=%d",
			q.admitCalls, r.fetches, r.transforms, len(q.completions))
	}
}

func TestShortSourceLeaseDefersWithoutStartingAnotherPaidStage(t *testing.T) {
	for _, denial := range []int{1, 2} {
		t.Run(fmt.Sprintf("stage-%d", denial), func(t *testing.T) {
			q := &stageQueue{fakeQueue: newFakeQueue(), admitErrAt: denial,
				admitErr: &cairn.APIError{StatusCode: http.StatusConflict, Code: "lease_released"}}
			r := &stageReader{q: q}
			p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
			job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease"}
			if err := p.Process(context.Background(), job); !errors.Is(err, ErrJobDeferred) {
				t.Fatalf("short lease = %v, want deferred", err)
			}
			if r.fetches != denial-1 || r.transforms != 0 || len(q.failures) != 0 || len(q.completions) != 0 {
				t.Fatalf("paid stages/failure after lease release: fetch=%d transform=%d failures=%d completions=%d",
					r.fetches, r.transforms, len(q.failures), len(q.completions))
			}
			if q.admitCalls != denial {
				t.Fatalf("stage admission calls = %d, want %d", q.admitCalls, denial)
			}
		})
	}
}

func TestSchedulerDoesNotReportReleasedClaimAsCompletedOrFailed(t *testing.T) {
	job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease"}
	q := &stageQueue{fakeQueue: newFakeQueue(job), admitErr: &cairn.APIError{StatusCode: http.StatusConflict, Code: "lease_released"}}
	p := NewStaged(q, &stageReader{q: q}, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	stats, err := p.Run(context.Background(), 1)
	if err != nil || stats.Claimed != 1 || stats.Completed != 0 || stats.Failed != 0 {
		t.Fatalf("released claim stats = %+v, err=%v", stats, err)
	}
}

type stageClassifier struct{ fail bool }

func (stageClassifier) SpecID() string { return "classify-v1" }

func (c stageClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	if c.fail {
		return classify.Result{}, errors.New("Jev unavailable")
	}
	return classify.Result{}, nil
}

func TestSourceSurvivesReadingFailureAndRetryDoesNotFetch(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	r := &stageReader{q: q, fail: true}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1}
	if err := p.Process(context.Background(), job); err == nil {
		t.Fatal("expected reading failure")
	}
	if q.source == nil || r.fetches != 1 {
		t.Fatal("source not saved")
	}
	if done, failed, err := p.RunClassifications(context.Background(), 1); err != nil || done != 1 || failed != 0 {
		t.Fatalf("classification blocked by reading: %d %d %v", done, failed, err)
	}
	r.fail = false
	job.Attempt = 2
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if r.fetches != 1 || q.completions[1].Classification != nil {
		t.Fatal("retry fetched or reading wrote classification")
	}
}

func TestClassificationFailureDoesNotFailSourceJob(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), source: &enrich.Source{OriginalText: "saved"}, job: &cairn.ClassificationJob{ID: 1}}
	p := NewStaged(q, nil, stageClassifier{fail: true}, "v1", "jev", discardLogger(), 1)
	done, failed, err := p.RunClassifications(context.Background(), 1)
	if err != nil || done != 0 || failed != 1 || q.classificationFailures != 1 || len(q.failures) != 0 || q.source.OriginalText != "saved" {
		t.Fatalf("classification failure leaked into source: %d %d %v", done, failed, err)
	}
}

// classifiedClassifier returns a caller-controlled error so the batch loop's
// handling of each runtime class can be exercised without a live provider.
type classifiedClassifier struct{ err error }

func (classifiedClassifier) SpecID() string { return "classify-v1" }

func (c classifiedClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	return classify.Result{}, c.err
}

func TestClassificationConfigurationErrorPausesInsteadOfBurningQueue(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	classErr := enrich.Classified(errors.New("bad key"), enrich.ErrorClassConfiguration)
	p := NewStaged(q, nil, classifiedClassifier{err: classErr}, "v1", "jev", discardLogger(), 1)
	done, failed, err := p.RunClassifications(context.Background(), 5)
	if err == nil {
		t.Fatal("configuration error should stop the batch")
	}
	if done != 0 || failed != 0 {
		t.Fatalf("configuration error counted as job failure: done=%d failed=%d", done, failed)
	}
	if !enrich.PausesComponent(err) {
		t.Fatalf("returned error does not pause the component: %v", err)
	}
}

func TestStaleClassificationIsNotAJobFailure(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	staleErr := enrich.Classified(errors.New("input changed"), enrich.ErrorClassStale)
	p := NewStaged(q, nil, classifiedClassifier{err: staleErr}, "v1", "jev", discardLogger(), 1)
	done, failed, err := p.RunClassifications(context.Background(), 1)
	if err != nil {
		t.Fatalf("stale classification should not abort the batch: %v", err)
	}
	if done != 0 || failed != 0 {
		t.Fatalf("stale classification counted as failure: done=%d failed=%d", done, failed)
	}
	if q.classificationFailures != 1 {
		t.Fatalf("stale classification was not reported to the Worker: %d", q.classificationFailures)
	}
}

func TestProviderOverloadStopsBatchBeforeDrainingQueuedLeases(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), jobPool: 5}
	overload := enrich.ClassifyModelError(&enrich.ModelHTTPError{StatusCode: 529, RetryAfter: 8 * time.Minute})
	p := NewStaged(q, nil, classifiedClassifier{err: overload}, "v1", "jev", discardLogger(), 1)
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	p.stages.pause.now = func() time.Time { return now }
	done, failed, err := p.RunClassifications(context.Background(), 5)
	if !errors.Is(err, ErrComponentPaused) || done != 0 || failed != 1 || q.claims != 1 || q.classificationFailures != 1 {
		t.Fatalf("overload drained queue: done=%d failed=%d claims=%d reports=%d err=%v", done, failed, q.claims, q.classificationFailures, err)
	}
	if len(q.classificationRetryHints) != 1 || q.classificationRetryHints[0] != 8*time.Minute {
		t.Fatalf("provider retry hint was not reported durably: %v", q.classificationRetryHints)
	}
	if _, _, remaining := p.ClassificationPaused(); remaining != 8*time.Minute {
		t.Fatalf("provider hint did not hold local component pause: %s", remaining)
	}
	_, _, _ = p.RunClassifications(context.Background(), 5)
	if q.claims != 1 {
		t.Fatalf("backoff claimed %d jobs, want one", q.claims)
	}
	now = now.Add(9 * time.Minute)
	p.stages.classifier = stageClassifier{}
	done, failed, err = p.RunClassifications(context.Background(), 5)
	if err != nil || done != 1 || failed != 0 || q.claims != 2 || !p.stages.pause.isHealthy() {
		t.Fatalf("half-open recovery: done=%d failed=%d claims=%d err=%v", done, failed, q.claims, err)
	}
}

func TestAlreadyCompletedDoesNotConfirmThisOperation(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	q.completeErr = enrich.Classified(errors.New("already completed"), enrich.ErrorClassCompleted)
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	done, failed, err := p.RunClassifications(context.Background(), 1)
	if err == nil || done != 0 || failed != 1 || q.entitySubmissions != 0 || q.evidenceRequests != 0 {
		t.Fatalf("generic completed is not an operation confirmation: done=%d failed=%d err=%v", done, failed, err)
	}
}

func TestHalfOpenProbeAlwaysReleasesWithoutInventingRecovery(t *testing.T) {
	for _, scenario := range []string{"claim_503", "claim_network", "model_transient", "cancel_before_probe", "zero_jobs", "empty_queue", "model_success"} {
		t.Run(scenario, func(t *testing.T) {
			q := &stageQueue{fakeQueue: newFakeQueue()}
			p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
			now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
			p.stages.pause.now = func() time.Time { return now }
			p.stages.pause.trip("model authentication failed")
			now = now.Add(time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			maxJobs := 5
			switch scenario {
			case "claim_503":
				q.claimErr = enrich.Classified(&cairn.APIError{StatusCode: http.StatusServiceUnavailable}, enrich.ErrorClassTransient)
			case "claim_network":
				q.claimErr = enrich.Classified(io.ErrUnexpectedEOF, enrich.ErrorClassTransient)
			case "model_transient":
				q.jobPool = 5
				p.stages.classifier = classifiedClassifier{err: enrich.Classified(errors.New("provider overloaded"), enrich.ErrorClassTransient)}
			case "cancel_before_probe":
				cancel()
			case "zero_jobs":
				maxJobs = 0
			case "model_success":
				q.job = &cairn.ClassificationJob{ID: 1}
			}
			_, _, _ = p.RunClassifications(ctx, maxJobs)
			p.stages.pause.mu.Lock()
			probing := p.stages.pause.probing
			p.stages.pause.mu.Unlock()
			if probing {
				t.Fatal("probe ownership leaked after return")
			}
			if p.stages.pause.isHealthy() != (scenario == "model_success") {
				t.Fatalf("only actual provider success may clear its breaker: scenario=%s", scenario)
			}
			if scenario == "model_transient" && q.claims != 1 {
				t.Fatalf("one half-open probe drained %d jobs", q.claims)
			}
			// A later bounded probe can recover, regardless of the earlier exit.
			q.claimErr = nil
			q.jobPool = 0
			q.job = &cairn.ClassificationJob{ID: 99}
			p.stages.classifier = stageClassifier{}
			now = now.Add(time.Hour)
			done, failed, err := p.RunClassifications(context.Background(), 1)
			if err != nil || done != 1 || failed != 0 || !p.stages.pause.isHealthy() {
				t.Fatalf("subsequent probe failed to recover: done=%d failed=%d err=%v", done, failed, err)
			}
		})
	}
}

type cancelledProbeQueue struct {
	*stageQueue
	started chan struct{}
	once    sync.Once
}

func (q *cancelledProbeQueue) ClaimClassification(ctx context.Context, _, _, _ string) (*cairn.ClassificationJob, error) {
	q.once.Do(func() { close(q.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHalfOpenProbeReleasesAfterClaimIsCancelled(t *testing.T) {
	base := &stageQueue{fakeQueue: newFakeQueue()}
	queue := &cancelledProbeQueue{stageQueue: base, started: make(chan struct{})}
	p := NewStaged(queue, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	p.stages.pause.now = func() time.Time { return now }
	p.stages.pause.trip("model authentication failed")
	now = now.Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := p.RunClassifications(ctx, 5)
		done <- err
	}()
	select {
	case <-queue.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("half-open claim did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled claim error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled half-open claim did not return")
	}
	p.stages.pause.mu.Lock()
	probing := p.stages.pause.probing
	p.stages.pause.mu.Unlock()
	if probing || p.stages.pause.isHealthy() {
		t.Fatal("cancelled claim kept probe ownership or cleared the model fault")
	}
	// The next bounded probe can still recover once its backoff expires.
	p.stages.queue = base
	base.job = &cairn.ClassificationJob{ID: 1}
	now = now.Add(time.Hour)
	doneCount, failed, err := p.RunClassifications(context.Background(), 5)
	if err != nil || doneCount != 1 || failed != 0 || !p.stages.pause.isHealthy() {
		t.Fatalf("probe after cancellation: done=%d failed=%d err=%v", doneCount, failed, err)
	}
}

func TestSharedClassificationCooldownSuppressesRepeatedWorkerPolls(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(),
		claimErr: &cairn.APIError{StatusCode: http.StatusServiceUnavailable, Code: "component_paused", RetryAfter: "480"}}
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	p.stages.pause.now = func() time.Time { return now }
	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("shared gate was not reported: %v", err)
	}
	if paused, _, remaining := p.ClassificationPaused(); !paused || remaining != 8*time.Minute {
		t.Fatalf("shared cooldown was not cached: paused=%t remaining=%s", paused, remaining)
	}
	for range 3 {
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("shared gate cache was ignored: %v", err)
		}
	}
	if q.claims != 1 {
		t.Fatalf("shared cooldown polled Worker %d times", q.claims)
	}
}

func TestEmptyHalfOpenQueueKeepsFaultButDoesNotDelayNextCandidate(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	p.stages.pause.now = func() time.Time { return now }
	p.stages.pause.trip("provider overloaded")
	now = now.Add(pauseBaseBackoff)
	if done, failed, err := p.RunClassifications(context.Background(), 5); err != nil || done != 0 || failed != 0 {
		t.Fatalf("empty probe: done=%d failed=%d err=%v", done, failed, err)
	}
	if p.stages.pause.isHealthy() {
		t.Fatal("empty queue cleared provider failure")
	}
	q.job = &cairn.ClassificationJob{ID: 1}
	if done, failed, err := p.RunClassifications(context.Background(), 5); err != nil || done != 1 || failed != 0 {
		t.Fatalf("new candidate waited through another backoff: done=%d failed=%d err=%v", done, failed, err)
	}
}

func TestPersistentClassification401DoesNotStopSourceWork(t *testing.T) {
	base := newFakeQueue()
	q := &stageQueue{fakeQueue: base,
		claimErr: enrich.Classified(&cairn.APIError{StatusCode: http.StatusUnauthorized, Code: "configuration_error"},
			enrich.ErrorClassConfiguration)}
	r := &stageReader{q: q}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	p.stages.pause.now = func() time.Time { return now }
	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("first 401 did not pause classification: %v", err)
	}
	_, _, firstBackoff := p.ClassificationPaused()
	for range 3 {
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("classification resumed before backoff: %v", err)
		}
	}
	if q.claims != 1 {
		t.Fatalf("paused classification claimed %d times", q.claims)
	}
	previousBackoff := firstBackoff
	for probe := 2; probe <= 3; probe++ {
		now = now.Add(previousBackoff + time.Second)
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("half-open 401 probe %d did not extend pause: %v", probe, err)
		}
		_, _, backoff := p.ClassificationPaused()
		if q.claims != probe || backoff <= previousBackoff {
			t.Fatalf("probe %d: claims=%d backoff=%s previous=%s", probe, q.claims, backoff, previousBackoff)
		}
		previousBackoff = backoff
	}
	base.jobs = []*cairn.Job{{ID: 7, URL: "https://x.com/synthetic/status/7", LeaseToken: "lease"}}
	stats, err := p.RunSources(context.Background(), 1)
	if err != nil || stats.Completed != 1 || r.fetches != 1 || r.transforms != 1 || len(base.completions) != 1 {
		t.Fatalf("classification fault blocked source or reading: stats=%+v fetches=%d transforms=%d completions=%d err=%v",
			stats, r.fetches, r.transforms, len(base.completions), err)
	}
	if p.stages.pause.isHealthy() || q.claims != 3 {
		t.Fatal("source completion incorrectly cleared the classification fault")
	}
}

// TestPauseSurvivesPollsAndDoesNotClaim is the F09 regression: one component
// fault must stop every later poll from claiming and burning attempts, and the
// pause must clear only after a successful probe.
func TestPauseSurvivesPollsAndDoesNotClaim(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	classErr := enrich.Classified(errors.New("bad key"), enrich.ErrorClassConfiguration)
	p := NewStaged(q, nil, classifiedClassifier{err: classErr}, "v1", "jev", discardLogger(), 1)
	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("first poll should pause: %v", err)
	}
	if q.claims != 1 {
		t.Fatalf("claims after the fault = %d, want 1", q.claims)
	}
	// More jobs are queued and several ticks pass: nothing may be claimed.
	q.job = &cairn.ClassificationJob{ID: 2}
	for tick := 0; tick < 3; tick++ {
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("tick %d should stay paused: %v", tick, err)
		}
	}
	if q.claims != 1 {
		t.Fatalf("a paused component claimed more jobs: %d", q.claims)
	}
	if paused, reason, remaining := p.ClassificationPaused(); !paused || reason == "" || remaining <= 0 {
		t.Fatalf("pause state not reported: %v %q %v", paused, reason, remaining)
	}
	// Advance past the backoff with the provider reachable again: one probe runs
	// and success clears the pause.
	p.stages.classifier = stageClassifier{}
	p.stages.pause.now = func() time.Time { return time.Now().Add(time.Hour) }
	done, failed, err := p.RunClassifications(context.Background(), 5)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("recovery probe failed: done=%d failed=%d err=%v", done, failed, err)
	}
	if paused, _, _ := p.ClassificationPaused(); paused {
		t.Fatal("a successful probe must clear the pause")
	}
}

// TestClaimLevel401PausesWithoutConsumingJobs is the cross-lease variant: the
// fault happens before a lease exists, so no attempt is spent at all.
func TestClaimLevel401PausesWithoutConsumingJobs(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	q.claimErr = enrich.Classified(&cairn.APIError{StatusCode: 401, Code: "configuration_error"}, enrich.ErrorClassConfiguration)
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	for tick := 0; tick < 3; tick++ {
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("tick %d should pause: %v", tick, err)
		}
	}
	if q.claims != 1 {
		t.Fatalf("paused claim path was re-entered %d times", q.claims)
	}
	// The lease/attempt budget is untouched because no job was handed out.
	if q.classificationFailures != 0 {
		t.Fatalf("paused claim reported a job failure: %d", q.classificationFailures)
	}
}

// --- B09 extension wiring ---------------------------------------------------

type fakeJudge struct{ value float64 }

func (f fakeJudge) Judge(_ context.Context, _ any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	answers := make(map[string]classify.RawAnswer, len(questions))
	for id, q := range questions {
		value := f.value
		if q.Type == classify.TypeChoice {
			selected := "relevant"
			if value < .5 {
				selected = "none"
			}
			confidence := 1.0
			answers[id] = classify.RawAnswer{Type: classify.TypeChoice, Choice: &classify.ChoiceAnswer{Choice: selected, Probabilities: map[string]float64{"relevant": value, "none": 1 - value, "unknown": 0, "incidental": 0}}, Confidence: &confidence}
			continue
		}
		answers[id] = classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &value}}
	}
	return answers, nil
}

type staticTransport struct{ body string }

func (s staticTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

// TestExtensionsRunAfterClassificationWithoutFailingIt proves the opt-in
// extensions record their own state and close a real evidence gap, while a
// disabled service changes nothing.
func TestExtensionsRunAfterClassificationWithoutFailingIt(t *testing.T) {
	job := &cairn.ClassificationJob{
		ID: 1, Revision: 1, InputRevision: 2, SpecID: "classify-v1",
		RelatedLinks: []string{"https://allowed.example/article"},
		Input:        classify.Input{URL: "https://x.com/a/status/1", OriginalText: "Acme builds Widgets."},
	}
	q := &stageQueue{fakeQueue: newFakeQueue(), job: job}
	bindExtensionFixture(t, q, job)
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)

	// Disabled: no entity state, no evidence request.
	done, failed, err := p.RunClassifications(context.Background(), 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("baseline classification failed: %d %d %v", done, failed, err)
	}
	if q.entitySubmissions != 0 || q.evidenceRequests != 0 {
		t.Fatalf("disabled extensions ran: entities=%d requests=%d", q.entitySubmissions, q.evidenceRequests)
	}

	flags := extension.DefaultFlags()
	flags.Entities = true
	flags.Evidence = true
	policy := extension.DefaultFetchPolicy([]string{"allowed.example"})
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.95}),
		&http.Client{Transport: staticTransport{body: "<html><body>Fetched external article body.</body></html>"}}, policy)

	q.job = job
	done, failed, err = p.RunClassifications(context.Background(), 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("classification with extensions failed: %d %d %v", done, failed, err)
	}
	if q.entitySubmissions != 1 {
		t.Fatalf("entity state was not submitted: %d", q.entitySubmissions)
	}
	if q.entityState["state"] != string(extension.EntityCompletedNonempty) {
		t.Fatalf("entity state = %v", q.entityState["state"])
	}
	if q.evidenceRequests != 1 || q.evidence != 0 {
		t.Fatalf("classification should only persist evidence intent: requests=%d snapshots=%d", q.evidenceRequests, q.evidence)
	}
	p.RunEvidenceRecovery(context.Background(), 1)
	if q.evidenceRequests != 1 || q.evidence != 1 {
		t.Fatalf("evidence escalation did not run: requests=%d snapshots=%d", q.evidenceRequests, q.evidence)
	}
	if len(q.evidenceDecisions) != 1 || q.evidenceDecisions[0]["status"] != "completed" {
		t.Fatalf("evidence outcome = %+v", q.evidenceDecisions)
	}
	if q.retries != 1 {
		t.Fatalf("new evidence must re-arm classification: retries=%d", q.retries)
	}
	// A blocked fetch keeps the old content and reports blocked.
	q.evidenceDecisions = nil
	q.evidenceRequests, q.evidence, q.retries = 0, 0, 0
	q.job = job
	blockedPolicy := extension.DefaultFetchPolicy([]string{"other.example"})
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.95}), &http.Client{Transport: staticTransport{body: "x"}}, blockedPolicy)
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	p.RunEvidenceRecovery(context.Background(), 1)
	if len(q.evidenceDecisions) != 1 || q.evidenceDecisions[0]["status"] != "blocked" {
		t.Fatalf("a blocked fetch must be reported, not silently skipped: %+v", q.evidenceDecisions)
	}
	if q.evidence != 0 || q.retries != 0 {
		t.Fatalf("a blocked fetch must not change the stored snapshot or re-run: snapshots=%d retries=%d", q.evidence, q.retries)
	}
}

// TestCircuitBreakerBackoffGrowsAcrossFailedProbes is the R2-09 regression: a
// sustained model fault must not restart the backoff from 30s on every poll,
// and the recovery probes must be bounded.
func TestCircuitBreakerBackoffGrowsAcrossFailedProbes(t *testing.T) {
	now := time.Now()
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	classErr := enrich.Classified(errors.New("bad key"), enrich.ErrorClassConfiguration)
	p := NewStaged(q, nil, classifiedClassifier{err: classErr}, "v1", "jev", discardLogger(), 1)
	p.stages.pause.now = func() time.Time { return now }

	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("first failure should pause: %v", err)
	}
	_, _, firstRemaining := p.ClassificationPaused()
	if q.claims != 1 {
		t.Fatalf("claims = %d, want 1", q.claims)
	}
	// A poll inside the backoff must not claim.
	for tick := 0; tick < 2; tick++ {
		if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
			t.Fatalf("tick %d should stay paused: %v", tick, err)
		}
	}
	if q.claims != 1 {
		t.Fatalf("a paused component claimed more jobs: %d", q.claims)
	}
	// Advance past the backoff: one probe claims exactly one job, fails, and the
	// next backoff is strictly longer.
	now = now.Add(firstRemaining + time.Second)
	q.job = &cairn.ClassificationJob{ID: 2}
	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("probe failure should stay paused: %v", err)
	}
	if q.claims != 2 {
		t.Fatalf("the probe must claim exactly one job: claims=%d", q.claims)
	}
	_, _, secondRemaining := p.ClassificationPaused()
	if secondRemaining <= firstRemaining {
		t.Fatalf("backoff did not grow: first=%s second=%s", firstRemaining, secondRemaining)
	}
	// Further polls inside the longer backoff still claim nothing.
	for tick := 0; tick < 3; tick++ {
		_, _, _ = p.RunClassifications(context.Background(), 5)
	}
	if q.claims != 2 {
		t.Fatalf("sustained faults consumed extra jobs: claims=%d", q.claims)
	}
}

// TestCircuitBreakerProbeIsAtomic proves concurrent callers cannot both take
// the single half-open probe.
func TestCircuitBreakerProbeIsAtomic(t *testing.T) {
	now := time.Now()
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	classErr := enrich.Classified(errors.New("bad key"), enrich.ErrorClassConfiguration)
	p := NewStaged(q, nil, classifiedClassifier{err: classErr}, "v1", "jev", discardLogger(), 1)
	p.stages.pause.now = func() time.Time { return now }
	if _, _, err := p.RunClassifications(context.Background(), 5); !errors.Is(err, ErrComponentPaused) {
		t.Fatalf("expected pause: %v", err)
	}
	now = now.Add(time.Hour)
	// Enough jobs for everyone: the probe limit, not the queue, must bound the
	// claims.
	claimsBeforeProbe := q.claims
	q.jobPool = 10
	var wg sync.WaitGroup
	results := make([]bool, 4)
	for index := range results {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, _, err := p.RunClassifications(context.Background(), 1)
			results[slot] = errors.Is(err, ErrComponentPaused)
		}(index)
	}
	wg.Wait()
	pausedCount := 0
	for _, paused := range results {
		if paused {
			pausedCount++
		}
	}
	if pausedCount < 3 {
		t.Fatalf("concurrent probes were not limited: paused=%d of 4", pausedCount)
	}
	if q.claims > claimsBeforeProbe+1 {
		t.Fatalf("more than one probe claimed a job: claims=%d (before probe %d)", q.claims, claimsBeforeProbe)
	}
}

// reusingClassifier records whether the partial-reuse path was taken.
type reusingClassifier struct {
	classifyCalls int
	reuseCalls    int
	previous      *classify.RawJudgments
}

func (c *reusingClassifier) SpecID() string { return "classify-v1" }
func (c *reusingClassifier) Classify(context.Context, classify.Input) (classify.Result, error) {
	c.classifyCalls++
	return classify.Result{}, nil
}
func (c *reusingClassifier) ClassifyReusing(_ context.Context, _ classify.Input, previous *classify.RawJudgments, _ string) (classify.Result, error) {
	c.reuseCalls++
	c.previous = previous
	return classify.Result{}, nil
}

// TestPartialReuseIsOptInAndFallsBackSafely is the R2-13 production wiring test.
func TestPartialReuseIsOptInAndFallsBackSafely(t *testing.T) {
	catalog := taxonomyCatalogForReuse(t)
	spec, err := classify.CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := classify.MarshalSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]classify.RawAnswer{}
	probability := 0.9
	for _, question := range spec.Questions {
		switch question.Kind {
		case classify.QuestionNoul:
			answers[question.ID] = classify.RawAnswer{Type: classify.TypeNoul, Noul: &classify.NoulAnswer{Noul: &probability}}
		case classify.QuestionChoice:
			options := question.AnswerOptions()
			distribution := map[string]float64{}
			for index, option := range options {
				if index == 0 {
					distribution[option] = 1
				} else {
					distribution[option] = 0
				}
			}
			answers[question.ID] = classify.RawAnswer{Type: classify.TypeChoice,
				Choice: &classify.ChoiceAnswer{Choice: options[0], Probabilities: distribution}}
		}
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(classify.DefaultPolicy())
	run := &cairn.StoredRun{
		ID: 9, SpecID: spec.SpecID, SpecHash: spec.SemanticHash, RequestedModel: "jev",
		ResolvedModel: "jev", PolicyVersion: classify.PolicyVersion, Policy: policy,
		Answers: encoded, Coverage: "complete", Status: "succeeded",
	}
	job := &cairn.ClassificationJob{ID: 1, Revision: 1, SpecID: spec.SpecID,
		Input: classify.Input{OriginalText: "same evidence"}}
	q := &stageQueue{fakeQueue: newFakeQueue(), job: job, latestRun: run,
		storedSpec: cairn.StoredQuestionSpec{SpecID: spec.SpecID, Payload: payload}}
	classifier := &reusingClassifier{}
	p := NewStaged(q, nil, classifier, "v1", "jev", discardLogger(), 1)

	// Disabled by default: the full evaluation runs and no run is fetched.
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.classifyCalls != 1 || classifier.reuseCalls != 0 {
		t.Fatalf("default path must not reuse: classify=%d reuse=%d", classifier.classifyCalls, classifier.reuseCalls)
	}

	// A legacy run without recorded identity must not become reusable just
	// because its answers and current spec can be decoded.
	p.SetPartialReuse(true)
	q.job = job
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.classifyCalls != 2 || classifier.reuseCalls != 0 {
		t.Fatal("unknown legacy identity reused")
	}
	// Produce the compatible metadata through the real HTTP classifier.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev", "answers": answers, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2}})
	}))
	defer server.Close()
	liveClient, err := classify.NewClient(server.URL, "fixture", "jev", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := liveClient.Classify(context.Background(), job.Input)
	if err != nil {
		t.Fatal(err)
	}
	run.RawJudgments, err = json.Marshal(actual.RawJudgments)
	if err != nil {
		t.Fatal(err)
	}
	// Enabled with a compatible stored run: the reuse path is taken.
	p.SetPartialReuse(true)
	q.job = job
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.reuseCalls != 1 {
		t.Fatalf("opt-in reuse was not used: classify=%d reuse=%d", classifier.classifyCalls, classifier.reuseCalls)
	}
	if classifier.previous == nil || len(classifier.previous.Judgments) != len(spec.Questions) || classifier.previous.SourceRunID != 9 || len(classifier.previous.Calls) != 1 {
		t.Fatalf("previous judgments were not reconstructed: %+v", classifier.previous)
	}

	// An incompatible stored run falls back to the full evaluation.
	q.job = job
	q.latestRun = &cairn.StoredRun{ID: 10, SpecID: spec.SpecID, Status: "partial", Coverage: "partial", Answers: encoded}
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.classifyCalls != 3 {
		t.Fatalf("an incompatible run must fall back: classify=%d reuse=%d", classifier.classifyCalls, classifier.reuseCalls)
	}
}

func taxonomyCatalogForReuse(t *testing.T) taxonomy.Catalog {
	t.Helper()
	return taxonomy.Catalog{
		Version: "v1",
		Topics:  []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}},
		Forms:   []taxonomy.Term{{ID: "method", Label: "方法", Active: true}},
		Uses:    []taxonomy.Term{{ID: "try", Label: "待试", Active: true}},
	}
}

// recordingClassifier captures the input it was asked to evaluate.
type recordingClassifier struct {
	last  classify.Input
	calls int
}

func (c *recordingClassifier) SpecID() string { return "classify-v1" }
func (c *recordingClassifier) Classify(_ context.Context, input classify.Input) (classify.Result, error) {
	c.calls++
	c.last = input
	return classify.Result{}, nil
}

type deadlineClassifier struct{ deadline time.Time }

func (*deadlineClassifier) SpecID() string { return "classify-v1" }
func (c *deadlineClassifier) Classify(ctx context.Context, _ classify.Input) (classify.Result, error) {
	c.deadline, _ = ctx.Deadline()
	return classify.Result{}, nil
}

type deadlineCompletionQueue struct {
	*stageQueue
	deadline time.Time
	ctxErr   error
}

func (q *deadlineCompletionQueue) CompleteClassification(ctx context.Context, job *cairn.ClassificationJob, result classify.Result) error {
	q.deadline, _ = ctx.Deadline()
	q.ctxErr = ctx.Err()
	return q.stageQueue.CompleteClassification(ctx, job, result)
}

func TestClassificationCommitKeepsDeadlineAfterInference(t *testing.T) {
	queue := &deadlineCompletionQueue{stageQueue: &stageQueue{
		fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1},
	}}
	classifier := &deadlineClassifier{}
	worker := NewStaged(queue, nil, classifier, "v1", "jev", discardLogger(), 1)
	completed, failed, err := worker.RunClassifications(context.Background(), 1)
	if err != nil || completed != 1 || failed != 0 {
		t.Fatalf("classification completed=%d failed=%d error=%v", completed, failed, err)
	}
	if classifier.deadline.IsZero() || queue.deadline.IsZero() || queue.ctxErr != nil {
		t.Fatalf("inference deadline=%s completion deadline=%s completion context=%v",
			classifier.deadline, queue.deadline, queue.ctxErr)
	}
	if got := queue.deadline.Sub(classifier.deadline); got != classificationCommitMargin {
		t.Fatalf("completion has %s after inference, want %s", got, classificationCommitMargin)
	}
	if remaining := time.Until(queue.deadline); remaining < DefaultClassificationDeadline-time.Second {
		t.Fatalf("completion deadline already consumed: %s remains", remaining)
	}
}

type cutoffClassifier struct{}

func (cutoffClassifier) SpecID() string { return "classify-v1" }
func (cutoffClassifier) Classify(ctx context.Context, _ classify.Input) (classify.Result, error) {
	<-ctx.Done()
	// A response may arrive at the same boundary that cancels the HTTP
	// context. Its parsed result must still have a live completion context.
	return classify.Result{}, nil
}

func TestClassificationResultAtInferenceCutoffCanStillCommit(t *testing.T) {
	queue := &deadlineCompletionQueue{stageQueue: &stageQueue{
		fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1},
	}}
	worker := NewStaged(queue, nil, cutoffClassifier{}, "v1", "jev", discardLogger(), 1)
	worker.stages.classificationDeadline = classificationCommitMargin + 30*time.Millisecond
	completed, failed, err := worker.RunClassifications(context.Background(), 1)
	if err != nil || completed != 1 || failed != 0 || queue.ctxErr != nil {
		t.Fatalf("result at cutoff: completed=%d failed=%d error=%v completion context=%v",
			completed, failed, err, queue.ctxErr)
	}
}

func TestBoundEvidenceFailureNeverFallsBackToPlainText(t *testing.T) {
	good := `{"blocks":[{"id":"p","role":"primary","text":"bound material"}],"retrieval":"manual","truncation":{"truncated":false}}`
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(good)))
	valid := fmt.Sprintf(`{"id":7,"content_revision":2,"content_hash":%q,"snapshot":%s}`, hash, good)
	for _, tc := range []struct {
		name    string
		payload string
		readErr error
	}{
		{name: "404", readErr: errors.New("HTTP 404")},
		{name: "503", readErr: enrich.Classified(errors.New("HTTP 503"), enrich.ErrorClassTransient)},
		{name: "malformed", payload: `{"snapshot":`},
		{name: "wrong-id", payload: strings.Replace(valid, `"id":7`, `"id":8`, 1)},
		{name: "wrong-revision", payload: strings.Replace(valid, `"content_revision":2`, `"content_revision":3`, 1)},
		{name: "wrong-hash", payload: strings.Replace(valid, hash, strings.Repeat("0", 64), 1)},
		{name: "altered-body", payload: strings.Replace(valid, "bound material", "other material", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &stageQueue{fakeQueue: newFakeQueue(), evidenceReadErr: tc.readErr, evidenceSnapshot: json.RawMessage(tc.payload), job: &cairn.ClassificationJob{ID: 1, ContentRevision: 2, EvidenceSnapshotID: 7, EvidenceHash: hash, Input: classify.Input{OriginalText: "fallback material"}}}
			classifier := &recordingClassifier{}
			p := NewStaged(q, nil, classifier, "v1", "jev", discardLogger(), 1)
			done, failed, err := p.RunClassifications(context.Background(), 1)
			if done != 0 || classifier.calls != 0 || q.classified != 0 || (err == nil && failed == 0) {
				t.Fatalf("bad evidence reached inference: done=%d failed=%d calls=%d commits=%d err=%v", done, failed, classifier.calls, q.classified, err)
			}
		})
	}
}

func TestSourceCheckpointRetryRepairsMissingSnapshotWithoutFetch(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), evidenceErr: errors.New("snapshot unavailable")}
	r := &stageReader{q: q}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/synthetic/status/42", Attempt: 1}
	if err := p.Process(context.Background(), job); err == nil {
		t.Fatal("expected snapshot write failure")
	}
	if q.source == nil || len(q.completions) != 0 {
		t.Fatal("source checkpoint missing or reading completed early")
	}
	q.evidenceErr = nil
	job.Attempt = 2
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if r.fetches != 1 || q.evidence != 2 || len(q.completions) != 1 {
		t.Fatalf("retry did not repair checkpoint: fetches=%d snapshots=%d completions=%d", r.fetches, q.evidence, len(q.completions))
	}
	q.evidenceSnapshot = json.RawMessage(`{"current":true}`)
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if q.evidence != 2 {
		t.Fatal("current snapshot was unnecessarily replaced")
	}
}

func TestRefreshedSourceSnapshotFailureDoesNotAcknowledgeFailedFetch(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), evidenceErr: errors.New("snapshot unavailable")}
	r := &stageReader{q: q}
	p := NewStaged(q, r, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/synthetic/status/42", Attempt: 1, RefreshEpoch: 3}
	if err := p.Process(context.Background(), job); err == nil {
		t.Fatal("expected snapshot write failure")
	}
	if q.source == nil || q.source.OriginalText != "saved original" || q.refreshAcks != 0 ||
		q.failures[job.ID] != "[recovered_source] persist evidence snapshot: snapshot unavailable" {
		t.Fatalf("saved refresh was misreported: source=%+v acks=%d failure=%q",
			q.source, q.refreshAcks, q.failures[job.ID])
	}
	q.evidenceErr = nil
	job.Attempt = 2
	job.RefreshEpoch = 0 // The Worker consumed it with the successful source write.
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if r.fetches != 1 || q.refreshAcks != 0 || q.evidence != 2 || len(q.completions) != 1 {
		t.Fatalf("retry did not repair saved refresh: fetches=%d acks=%d snapshots=%d completions=%d",
			r.fetches, q.refreshAcks, q.evidence, len(q.completions))
	}
}

// TestRefreshIntentBypassesSourceCaches is the R2-06 regression: an explicit
// refresh must fetch, not reuse the stored snapshot or the legacy saved text.
func TestRefreshIntentBypassesSourceCaches(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), source: &enrich.Source{OriginalText: "old stored text", Model: "stored"}}
	r := &stageReader{q: q}
	p := NewStaged(q, r, &recordingClassifier{}, "v1", "jev", discardLogger(), 1)
	job := &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, RefreshEpoch: 3}
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatalf("refresh process: %v", err)
	}
	if r.fetches != 1 {
		t.Fatalf("an explicit refresh must fetch exactly once: %d", r.fetches)
	}
	if q.source == nil || q.source.OriginalText != "saved original" {
		t.Fatalf("the fetched source was not saved: %+v", q.source)
	}
	if q.refreshAcks != 0 {
		t.Fatalf("a saved source checkpoint needs no separate success ack: acks=%d", q.refreshAcks)
	}
	// A failing fetch keeps the old readable content and consumes the intent.
	q.source = &enrich.Source{OriginalText: "old stored text", Model: "stored"}
	failing := &stageReader{q: q, fetchErr: errors.New("fetch down")}
	p2 := NewStaged(q, failing, &recordingClassifier{}, "v1", "jev", discardLogger(), 1)
	if err := p2.Process(context.Background(), &cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, RefreshEpoch: 4}); err == nil {
		t.Fatal("a failing refresh must report an error")
	}
	if q.source.OriginalText != "old stored text" {
		t.Fatalf("a failed refresh must keep the old content: %+v", q.source)
	}
	if q.refreshAcks != 1 {
		t.Fatalf("a failed refresh must still consume the intent: acks=%d", q.refreshAcks)
	}
}

func TestFailedRefreshAckErrorIsNotSilentlyReportedAsHandled(t *testing.T) {
	ackErr := errors.New("fixture ack unavailable")
	q := &stageQueue{fakeQueue: newFakeQueue(), refreshAckErr: ackErr}
	p := NewStaged(q, &stageReader{q: q, fetchErr: errors.New("fixture fetch failed")},
		&recordingClassifier{}, "v1", "jev", discardLogger(), 1)
	err := p.Process(context.Background(), &cairn.Job{ID: 1, URL: "https://x.com/a/status/1",
		Attempt: 1, RefreshEpoch: 3})
	if !errors.Is(err, ackErr) || q.refreshAcks != 1 || len(q.failures) != 0 {
		t.Fatalf("unconfirmed refresh ack was hidden: error=%v acks=%d failures=%v", err, q.refreshAcks, q.failures)
	}
}

// TestBoundEvidenceUsesTheStructuredSnapshot is the R2-07 regression: the
// provider state comes from the stored blocks with their roles, not from a
// reconstruction of the plain text fields.
func TestBoundEvidenceUsesTheStructuredSnapshot(t *testing.T) {
	snapshot := json.RawMessage(`{
		"id": 7, "content_revision": 2, "content_hash": "abc", "completeness": "complete",
		"snapshot": {
			"blocks": [
				{"id": "primary-1", "role": "primary", "text": "primary body"},
				{"id": "external-1", "role": "external_article", "text": "unique external text", "url": "https://allowed.example/a"},
				{"id": "context-1", "role": "quoted", "text": "a quote"}
			],
			"fetched_at": "2026-09-21T00:00:00Z", "retrieval": "x_search", "truncation": {"truncated": false}
		}
	}`)
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, decoded["snapshot"]); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(compact.Bytes()))
	snapshot = bytes.Replace(snapshot, []byte(`"abc"`), []byte(`"`+hash+`"`), 1)
	q := &stageQueue{fakeQueue: newFakeQueue(), evidenceSnapshot: snapshot,
		job: &cairn.ClassificationJob{ID: 1, Revision: 1, ContentRevision: 2, EvidenceHash: hash, EvidenceSnapshotID: 7, Input: classify.Input{OriginalText: "plain text"}}}
	classifier := &recordingClassifier{}
	p := NewStaged(q, nil, classifier, "v1", "jev", discardLogger(), 1)
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.last.Evidence == nil {
		t.Fatal("the bound snapshot was not attached")
	}
	if classifier.last.Evidence.Primary != "primary body" {
		t.Fatalf("primary = %q", classifier.last.Evidence.Primary)
	}
	if len(classifier.last.Evidence.Context) != 2 {
		t.Fatalf("context blocks = %d, want 2", len(classifier.last.Evidence.Context))
	}
	if classifier.last.Evidence.Context[0].Role != classify.RoleExternalArticle ||
		classifier.last.Evidence.Context[0].Text != "unique external text" {
		t.Fatalf("the external block lost its role or text: %+v", classifier.last.Evidence.Context[0])
	}
}

// TestEvidenceEscalationSkipsDecidedRequests is the R2-07 dedupe regression.
func TestEvidenceEscalationSkipsDecidedRequests(t *testing.T) {
	job := &cairn.ClassificationJob{
		ID: 1, Revision: 1, InputRevision: 1,
		RelatedLinks: []string{"https://allowed.example/article"},
		Input:        classify.Input{OriginalText: "Acme builds Widgets."},
	}
	q := &stageQueue{fakeQueue: newFakeQueue(), job: job}
	bindExtensionFixture(t, q, job)
	flags := extension.DefaultFlags()
	flags.Entities = true
	flags.Evidence = true
	policy := extension.DefaultFetchPolicy([]string{"allowed.example"})
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.1}),
		&http.Client{Transport: staticTransport{body: "<html>fetched</html>"}}, policy)
	// The request already exists in a decided state: no fetch may happen.
	q.evidenceRequestStatus = "completed"
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if q.evidence != 0 || q.retries != 0 {
		t.Fatalf("a decided request must not fetch or re-queue: snapshots=%d retries=%d", q.evidence, q.retries)
	}
	if len(q.evidenceDecisions) != 0 {
		t.Fatalf("a decided request must not be decided again: %+v", q.evidenceDecisions)
	}
}

func bindExtensionFixture(t *testing.T, q *stageQueue, job *cairn.ClassificationJob) {
	t.Helper()
	snapshot, err := json.Marshal(map[string]any{"blocks": []map[string]string{
		{"id": "archived-primary", "role": "primary", "text": job.OriginalText},
	}, "truncation": map[string]bool{"truncated": false}})
	if err != nil {
		t.Fatal(err)
	}
	job.ContentRevision, job.EvidenceSnapshotID = 9, 27
	job.EvidenceHash = fmt.Sprintf("%x", sha256.Sum256(snapshot))
	q.evidenceSnapshot, err = json.Marshal(map[string]any{"id": job.EvidenceSnapshotID,
		"content_revision": job.ContentRevision, "content_hash": job.EvidenceHash, "snapshot": json.RawMessage(snapshot)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEntitiesRequireVerifiedSnapshot(t *testing.T) {
	job := &cairn.ClassificationJob{ID: 1, Revision: 2, InputRevision: 1, Input: classify.Input{OriginalText: "OldSource"}}
	q := &stageQueue{fakeQueue: newFakeQueue(), job: job}
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	flags := extension.DefaultFlags()
	flags.Entities = true
	p.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), fakeJudge{value: 0.95}), nil, extension.FetchPolicy{})
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if q.entitySubmissions != 0 {
		t.Fatal("unbound legacy source created a bound entity result")
	}
	q.job = job
	job.OriginalText = "ArchivedEntity"
	bindExtensionFixture(t, q, job)
	job.OriginalText = "OldSource"
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if q.entityState["content_revision"] != int64(9) || q.entityState["evidence_snapshot_id"] != int64(27) || q.entityState["content_hash"] != job.EvidenceHash {
		t.Fatalf("entity identity: %+v", q.entityState)
	}
	got := q.entityState["entities"].([]string)
	if len(got) != 1 || got[0] != "ArchivedEntity" {
		t.Fatalf("entities came from stale plain text: %v", got)
	}
}
