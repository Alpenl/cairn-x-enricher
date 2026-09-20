package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

type stageQueue struct {
	*fakeQueue
	source                 *enrich.Source
	job                    *cairn.ClassificationJob
	classificationFailures int
	classified             int
}

func (q *stageQueue) GetSource(context.Context, int64) (*enrich.Source, error) { return q.source, nil }
func (q *stageQueue) SaveSource(_ context.Context, id int64, _ string, s enrich.Source) error {
	q.source = &s
	q.job = &cairn.ClassificationJob{ID: id, Input: classify.Input{OriginalText: s.OriginalText}}
	return nil
}
func (q *stageQueue) ClaimClassification(context.Context, string, string) (*cairn.ClassificationJob, error) {
	j := q.job
	q.job = nil
	return j, nil
}
func (q *stageQueue) CompleteClassification(context.Context, *cairn.ClassificationJob, classify.Result) error {
	q.classified++
	return nil
}
func (q *stageQueue) FailClassification(context.Context, *cairn.ClassificationJob, string) error {
	q.classificationFailures++
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
