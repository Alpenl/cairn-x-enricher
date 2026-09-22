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
	mu                     sync.Mutex
	jobPool                int
	source                 *enrich.Source
	job                    *cairn.ClassificationJob
	classificationFailures int
	classified             int
	claims                 int
	claimErr               error
	completeErr            error
	evidence               int
	evidenceErr            error
	entityState            map[string]any
	entitySubmissions      int
	evidenceRequests       int
	evidenceDecisions      []map[string]any
	retries                int
	latestRun              *cairn.StoredRun
	storedSpec             cairn.StoredQuestionSpec
	evidenceSnapshot       json.RawMessage
	evidenceReadErr        error
	evidenceRequestStatus  string
	refreshAcks            int
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
func (q *stageQueue) FailClassification(context.Context, *cairn.ClassificationJob, string) error {
	q.classificationFailures++
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
func (q *stageQueue) CreateEvidenceRequest(context.Context, int64, map[string]any) (string, string, error) {
	q.evidenceRequests++
	status := q.evidenceRequestStatus
	if status == "" {
		status = "pending"
	}
	return "req-1", status, nil
}
func (q *stageQueue) GetEvidenceAt(context.Context, int64, int64) (json.RawMessage, error) {
	return q.evidenceSnapshot, q.evidenceReadErr
}
func (q *stageQueue) GetEvidence(context.Context, int64) (json.RawMessage, error) {
	return q.evidenceSnapshot, q.evidenceReadErr
}
func (q *stageQueue) AckSourceRefresh(context.Context, int64, int64, string, string) error {
	q.refreshAcks++
	return nil
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
	fetches  int
	fail     bool
	fetchErr error
	q        *stageQueue
}

func (r *stageReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	if r.fetchErr != nil {
		return enrich.Source{}, r.fetchErr
	}
	return enrich.Source{OriginalText: "saved original", Model: "grok", RelatedLinks: []string{}, ImageURLs: []string{}}, nil
}
func (r *stageReader) Transform(_ context.Context, i enrich.Input) (enrich.Result, error) {
	if r.q.source == nil {
		return enrich.Result{}, errors.New("reading started before source persisted")
	}
	if r.fail {
		return enrich.Result{}, errors.New("reading unavailable")
	}
	return enrich.Result{OriginalText: i.SourceText, Summary: "summary"}, nil
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
	for _, scenario := range []string{"claim_transient", "model_transient", "cancelled", "zero_jobs", "empty_queue", "model_success"} {
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
			case "claim_transient":
				q.claimErr = enrich.Classified(errors.New("claim unavailable"), enrich.ErrorClassTransient)
			case "model_transient":
				q.jobPool = 5
				p.stages.classifier = classifiedClassifier{err: enrich.Classified(errors.New("provider overloaded"), enrich.ErrorClassTransient)}
			case "cancelled":
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
	for id := range questions {
		value := f.value
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
		ResolvedModel: "jev", PolicyVersion: "jev-policy-v2", Policy: policy,
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

	// Enabled with a compatible stored run: the reuse path is taken.
	p.SetPartialReuse(true)
	q.job = job
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.reuseCalls != 1 {
		t.Fatalf("opt-in reuse was not used: classify=%d reuse=%d", classifier.classifyCalls, classifier.reuseCalls)
	}
	if classifier.previous == nil || len(classifier.previous.Judgments) != len(spec.Questions) {
		t.Fatalf("previous judgments were not reconstructed: %+v", classifier.previous)
	}

	// An incompatible stored run falls back to the full evaluation.
	q.job = job
	q.latestRun = &cairn.StoredRun{ID: 10, SpecID: spec.SpecID, Status: "partial", Coverage: "partial", Answers: encoded}
	if _, _, err := p.RunClassifications(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if classifier.classifyCalls != 2 {
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
	if q.refreshAcks != 1 {
		t.Fatalf("the refresh intent was not consumed: acks=%d", q.refreshAcks)
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
	if q.refreshAcks != 2 {
		t.Fatalf("a failed refresh must still consume the intent: acks=%d", q.refreshAcks)
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
