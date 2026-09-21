package processor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

type stageQueue struct {
	*fakeQueue
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
}

func (q *stageQueue) GetSource(context.Context, int64) (*enrich.Source, error) { return q.source, nil }
func (q *stageQueue) SaveSource(_ context.Context, id int64, _ string, s enrich.Source) error {
	q.source = &s
	q.job = &cairn.ClassificationJob{ID: id, Input: classify.Input{OriginalText: s.OriginalText}}
	return nil
}
func (q *stageQueue) ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error) {
	q.claims++
	if q.claimErr != nil {
		return nil, q.claimErr
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
func (q *stageQueue) CreateEvidenceRequest(context.Context, int64, map[string]any) (string, error) {
	q.evidenceRequests++
	return "req-1", nil
}
func (q *stageQueue) DecideEvidenceRequest(_ context.Context, _ string, body map[string]any) error {
	q.evidenceDecisions = append(q.evidenceDecisions, body)
	return nil
}
func (q *stageQueue) RetryClassification(context.Context, int64) error {
	q.retries++
	return nil
}

type stageReader struct {
	fetches int
	fail    bool
	q       *stageQueue
}

func (r *stageReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
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

func TestAlreadyCompletedCompletionCountsAsSuccess(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 1}}
	q.completeErr = enrich.Classified(errors.New("already completed"), enrich.ErrorClassCompleted)
	p := NewStaged(q, nil, stageClassifier{}, "v1", "jev", discardLogger(), 1)
	done, failed, err := p.RunClassifications(context.Background(), 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("lost completion response should be success: done=%d failed=%d err=%v", done, failed, err)
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
