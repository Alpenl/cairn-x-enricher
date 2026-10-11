package processor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

type fakeQueue struct {
	mu          sync.Mutex
	jobs        []*cairn.Job
	claimErr    error
	details     map[int64]cairn.BookmarkDetail
	completions map[int64]cairn.Completion
	failures    map[int64]string
	imageURLs   map[int64][]string
	imageErr    error
	failErr     error
}

func (q *fakeQueue) Claim(context.Context) (*cairn.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.jobs) == 0 {
		return nil, nil
	}
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, nil
}

func (q *fakeQueue) GetBookmark(_ context.Context, id int64) (cairn.BookmarkDetail, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.details[id], nil
}

func (q *fakeQueue) Complete(_ context.Context, id int64, completion cairn.Completion) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completions[id] = completion
	return nil
}

func (q *fakeQueue) StoreImages(_ context.Context, id int64, _ string, imageURLs []string) ([]cairn.ImageRef, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.imageErr != nil {
		return nil, q.imageErr
	}
	q.imageURLs[id] = append([]string(nil), imageURLs...)
	return []cairn.ImageRef{{
		Key:         "enrichment/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg",
		ContentType: "image/jpeg",
	}}, nil
}

func (q *fakeQueue) Fail(_ context.Context, id int64, _ string, message string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failErr != nil {
		return q.failErr
	}
	q.failures[id] = message
	return nil
}

type fakeEnricher struct {
	failID  int64
	inputs  chan enrich.Input
	noMedia bool
}

type deadlineProbeEnricher struct {
	called   int
	deadline time.Time
}

type capacityProbeEnricher struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (e *capacityProbeEnricher) Enrich(ctx context.Context, input enrich.Input) (enrich.Result, error) {
	if input.ID == 1 {
		close(e.firstStarted)
		select {
		case <-e.releaseFirst:
		case <-ctx.Done():
			return enrich.Result{}, ctx.Err()
		}
	}
	return fakeEnricher{noMedia: true}.Enrich(ctx, input)
}

type claimProbeQueue struct {
	*fakeQueue
	firstClaim chan struct{}
	once       sync.Once
}

func TestClassificationOnlyRoundLeavesNewSourceUnclaimed(t *testing.T) {
	queue := newFakeQueue(&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	worker := New(queue, nil, discardLogger(), 1)
	stats, err := worker.RunClassificationsOnly(context.Background(), 1)
	if err != nil || stats.HasWork() || len(queue.jobs) != 1 {
		t.Fatalf("classification-only round claimed a source: stats=%+v jobs=%d error=%v",
			stats, len(queue.jobs), err)
	}
}

func (q *claimProbeQueue) Claim(ctx context.Context) (*cairn.Job, error) {
	q.once.Do(func() { close(q.firstClaim) })
	return q.fakeQueue.Claim(ctx)
}

func TestSourceSweepAcquiresCapacityBeforeClaimingLease(t *testing.T) {
	queue := &claimProbeQueue{fakeQueue: newFakeQueue(&cairn.Job{ID: 2,
		URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "lease-2"}), firstClaim: make(chan struct{})}
	queue.details[1] = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{OriginalText: "archived source"}}
	model := &capacityProbeEnricher{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	worker := New(queue, model, discardLogger(), 1)
	manualDone := make(chan error, 1)
	go func() {
		manualDone <- worker.Process(context.Background(), &cairn.Job{ID: 1,
			URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease-1"})
	}()
	select {
	case <-model.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("manual job did not acquire capacity")
	}
	batchDone := make(chan error, 1)
	go func() { _, err := worker.RunSources(context.Background(), 1); batchDone <- err }()
	select {
	case <-queue.firstClaim:
		t.Fatal("source sweep claimed a lease while capacity was held")
	case <-time.After(50 * time.Millisecond):
	}
	close(model.releaseFirst)
	if err := <-manualDone; err != nil {
		t.Fatalf("manual job failed: %v", err)
	}
	select {
	case err := <-batchDone:
		if err != nil {
			t.Fatalf("source sweep failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source sweep did not resume after capacity was released")
	}
	select {
	case <-queue.firstClaim:
	default:
		t.Fatal("source sweep never claimed after capacity was released")
	}
}

func (e *deadlineProbeEnricher) Enrich(ctx context.Context, _ enrich.Input) (enrich.Result, error) {
	e.called++
	e.deadline, _ = ctx.Deadline()
	return enrich.Result{}, errors.New("fixture stopped before a provider call")
}

func TestSourceJobIsBoundedByItsLeaseAndRejectsExpiredWork(t *testing.T) {
	queue := newFakeQueue()
	queue.details[12] = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{OriginalText: "archived source"}}
	model := &deadlineProbeEnricher{}
	worker := New(queue, model, discardLogger(), 1)
	leaseUntil := time.Now().Add(2 * time.Minute)
	job := &cairn.Job{ID: 12, URL: "https://x.com/a/status/12", Attempt: 1,
		LeaseToken: "lease-12", LeaseUntil: leaseUntil.Format(time.RFC3339Nano)}
	if err := worker.Process(context.Background(), job); err == nil || model.called != 1 {
		t.Fatalf("source call=%d error=%v", model.called, err)
	}
	want := leaseUntil
	if model.deadline.IsZero() || model.deadline.Before(want.Add(-time.Second)) || model.deadline.After(want.Add(time.Second)) {
		t.Fatalf("source deadline=%s, want near %s", model.deadline, want)
	}
	job.LeaseUntil = time.Now().Add(20 * time.Second).Format(time.RFC3339Nano)
	if err := worker.Process(context.Background(), job); !enrich.IsStale(err) || model.called != 1 {
		t.Fatalf("expired source was called: calls=%d error=%v", model.called, err)
	}
	job.LeaseUntil = "invalid"
	if err := worker.Process(context.Background(), job); !enrich.PausesComponent(err) || model.called != 1 {
		t.Fatalf("malformed lease was used: calls=%d error=%v", model.called, err)
	}
}

func (e fakeEnricher) Enrich(_ context.Context, input enrich.Input) (enrich.Result, error) {
	if e.inputs != nil {
		e.inputs <- input
	}
	if input.ID == e.failID {
		return enrich.Result{}, errors.New("model failure")
	}
	originalText := "source"
	if input.SourceText != "" {
		originalText = input.SourceText
	}
	relatedLinks := []string{"https://example.com/source"}
	imageURLs := []string{"https://pbs.twimg.com/media/source?format=jpg"}
	if e.noMedia {
		relatedLinks = nil
		imageURLs = nil
	}
	return enrich.Result{
		AITitle:          "人工智能生成的测试中文标题",
		OriginalLanguage: "en",
		OriginalText:     originalText,
		TranslatedText:   "中文译文",
		Summary:          "summary",
		RelatedLinks:     relatedLinks,
		ImageURLs:        imageURLs,
		Model:            "grok-test",
	}, nil
}

func TestRunCompletesClaimedJobs(t *testing.T) {
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease-1"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "lease-2"},
	)
	processor := New(queue, fakeEnricher{}, discardLogger(), 2)

	stats, err := processor.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats.Claimed != 2 || stats.Completed != 2 || stats.Failed != 0 {
		t.Fatalf("Run() stats = %+v", stats)
	}
	if len(queue.completions) != 2 || queue.completions[1].LeaseToken != "lease-1" {
		t.Fatalf("completions = %#v", queue.completions)
	}
	if len(queue.imageURLs[1]) != 1 || len(queue.completions[1].Images) != 1 {
		t.Fatalf("image persistence = URLs %#v, completion %#v", queue.imageURLs, queue.completions[1])
	}
}

func TestRunContinuesAfterOneBookmarkFails(t *testing.T) {
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "lease-1"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", Attempt: 1, LeaseToken: "lease-2"},
	)
	processor := New(queue, fakeEnricher{failID: 1}, discardLogger(), 1)

	stats, err := processor.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stats.Claimed != 2 || stats.Completed != 1 || stats.Failed != 1 {
		t.Fatalf("Run() stats = %+v", stats)
	}
	// The stored message names the path so a retrieval failure is
	// distinguishable from a failure to reformat already-stored text.
	if queue.failures[1] != "[recovered_source] model failure" {
		t.Fatalf("failure = %q, want the path-labelled cause", queue.failures[1])
	}
	if len(queue.jobs) != 0 || len(queue.completions) != 1 {
		t.Fatalf("other bookmark was not processed: pending=%d complete=%d", len(queue.jobs), len(queue.completions))
	}
}

type hangingClaimQueue struct{ Queue }

func (hangingClaimQueue) Claim(ctx context.Context) (*cairn.Job, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEachSourceClaimHasItsOwnTimeout(t *testing.T) {
	p := New(hangingClaimQueue{}, fakeEnricher{}, discardLogger(), 1)
	p.SetClaimTimeout(30 * time.Millisecond)
	started := time.Now()
	_, err := p.RunSources(context.Background(), 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("claim error = %v, want request timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("single claim took %s despite its timeout", elapsed)
	}
}

func TestWorkerWriteFailureStopsSourceClaims(t *testing.T) {
	queue := newFakeQueue(
		&cairn.Job{ID: 1, URL: "https://x.com/a/status/1", LeaseToken: "one"},
		&cairn.Job{ID: 2, URL: "https://x.com/a/status/2", LeaseToken: "two"},
	)
	queue.failErr = &cairn.APIError{StatusCode: 503, Code: "upstream_unavailable"}
	p := New(queue, fakeEnricher{failID: 1}, discardLogger(), 1)
	stats, err := p.RunSources(context.Background(), 10)
	if err == nil || stats.Claimed != 1 || len(queue.jobs) != 1 {
		t.Fatalf("Worker failure consumed unrelated leases: stats=%+v pending=%d err=%v", stats, len(queue.jobs), err)
	}
}

func TestRunReturnsClaimError(t *testing.T) {
	want := errors.New("backend unavailable")
	queue := newFakeQueue()
	queue.claimErr = want
	processor := New(queue, fakeEnricher{}, discardLogger(), 1)

	_, err := processor.Run(context.Background(), 1)
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, want)
	}
}

func TestProcessReportsImagePersistenceFailure(t *testing.T) {
	queue := newFakeQueue()
	queue.details[9] = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{OriginalText: "archived source"}}
	queue.imageErr = errors.New("R2 unavailable")
	processor := New(queue, fakeEnricher{}, discardLogger(), 1)
	job := &cairn.Job{ID: 9, URL: "https://x.com/a/status/9", Attempt: 1, LeaseToken: "lease-9"}

	if err := processor.Process(context.Background(), job); err == nil {
		t.Fatal("Process() error = nil, want image persistence error")
	}
	if queue.failures[9] != "[recovered_source] store enrichment images: R2 unavailable" {
		t.Fatalf("failure = %q, want the path-labelled cause", queue.failures[9])
	}
	if _, exists := queue.completions[9]; exists {
		t.Fatal("completion was stored after image persistence failed")
	}
}

func TestProcessRecoversPartialBookmarkFromExistingSourceText(t *testing.T) {
	queue := newFakeQueue()
	queue.details[12] = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{
		ID: 12, URL: "https://x.com/a/status/12", Status: "exhausted",
		OriginalText: "已有原帖正文",
		Summary:      "已有摘要",
		RelatedURLs:  []string{"https://example.com/thread"},
		Images: []cairn.ImageRef{{
			Key:         "enrichment/12/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg",
			ContentType: "image/jpeg",
		}},
	}}
	inputs := make(chan enrich.Input, 1)
	processor := New(queue, fakeEnricher{inputs: inputs, noMedia: true}, discardLogger(), 1)
	job := &cairn.Job{ID: 12, URL: "https://x.com/a/status/12", Attempt: 5, LeaseToken: "lease-12"}

	if err := processor.Process(context.Background(), job); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	input := <-inputs
	if input.SourceText != "已有原帖正文" {
		t.Fatalf("SourceText = %q", input.SourceText)
	}
	completion := queue.completions[12]
	if completion.OriginalText != "已有原帖正文" || completion.TranslatedText == "" {
		t.Fatalf("completion = %+v", completion)
	}
	if len(completion.RelatedLinks) != 1 || completion.RelatedLinks[0] != "https://example.com/thread" {
		t.Fatalf("RelatedLinks = %#v", completion.RelatedLinks)
	}
	if len(completion.Images) != 1 {
		t.Fatalf("Images = %#v", completion.Images)
	}
	if len(queue.imageURLs[12]) != 0 {
		t.Fatalf("image URLs stored = %#v", queue.imageURLs[12])
	}
}

func TestProcessWithSourceUsesManualSourceText(t *testing.T) {
	queue := newFakeQueue()
	inputs := make(chan enrich.Input, 1)
	processor := New(queue, fakeEnricher{inputs: inputs, noMedia: true}, discardLogger(), 1)
	job := &cairn.Job{ID: 20, URL: "https://x.com/a/status/20", Attempt: 6, LeaseToken: "lease-20"}

	if err := processor.ProcessWithSource(context.Background(), job, " 人工粘贴原文 "); err != nil {
		t.Fatalf("ProcessWithSource() error = %v", err)
	}
	input := <-inputs
	if input.SourceText != "人工粘贴原文" {
		t.Fatalf("SourceText = %q", input.SourceText)
	}
	if queue.completions[20].OriginalText != "人工粘贴原文" {
		t.Fatalf("completion = %+v", queue.completions[20])
	}
}

func newFakeQueue(jobs ...*cairn.Job) *fakeQueue {
	queue := &fakeQueue{
		jobs:        jobs,
		details:     make(map[int64]cairn.BookmarkDetail),
		completions: make(map[int64]cairn.Completion),
		failures:    make(map[int64]string),
		imageURLs:   make(map[int64][]string),
	}
	for _, job := range jobs {
		queue.details[job.ID] = cairn.BookmarkDetail{Bookmark: cairn.Bookmark{ID: job.ID, OriginalText: "archived source", OriginalLanguage: "en"}}
	}
	return queue
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
