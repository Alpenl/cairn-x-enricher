package processor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
)

type localPauseQueue struct {
	*stageQueue
	storedID     int64
	storedSource *enrich.Source
	claimMasks   [][2]bool
}

type panickingSourceReader struct{ *stageReader }

func (r *panickingSourceReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	panic("source provider adapter failed")
}

func (q *localPauseQueue) ClaimAllowed(_ context.Context, source, reading bool) (*cairn.Job, error) {
	q.fakeQueue.mu.Lock()
	defer q.fakeQueue.mu.Unlock()
	q.claimMasks = append(q.claimMasks, [2]bool{source, reading})
	for i, job := range q.jobs {
		if job.SourceComponent == "source" && source || job.SourceComponent == "reading" && reading {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			return job, nil
		}
	}
	return nil, nil
}

func (q *localPauseQueue) GetSource(_ context.Context, id int64) (*enrich.Source, error) {
	if id == q.storedID {
		return q.storedSource, nil
	}
	return nil, nil
}

func TestLocalSourceContractPauseSkipsOnlySourceAndRecoversOnOneProbe(t *testing.T) {
	const url = "https://x.com/u/status/1"
	base := &stageQueue{fakeQueue: newFakeQueue(
		&cairn.Job{ID: 1, URL: url, LeaseToken: "source-1", Attempt: 1, SourceComponent: "source"},
		&cairn.Job{ID: 2, URL: url, LeaseToken: "reading-2", Attempt: 1, SourceComponent: "reading"})}
	base.source = &enrich.Source{OriginalText: "stored text", RelatedLinks: []string{}}
	q := &localPauseQueue{stageQueue: base, storedID: 2, storedSource: base.source}
	providerFault := enrich.Classified(errors.New("bad source schema"), enrich.ErrorClassContract)
	reader := &stageReader{q: base, fetchErr: providerFault}
	var logs bytes.Buffer
	p := NewStaged(q, reader, nil, "", "",
		slog.New(observability.SafeJSONHandler(&logs, slog.LevelDebug)), 1)
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	p.stages.sourcePause.now = func() time.Time { return now }
	stats, err := p.RunSources(context.Background(), 3)
	if err != nil || stats.Claimed != 2 || stats.Failed != 1 || stats.Completed != 1 ||
		reader.fetches != 1 || reader.transforms != 1 {
		t.Fatalf("independent stage round = %+v, fetches=%d reading=%d err=%v",
			stats, reader.fetches, reader.transforms, err)
	}
	if paused, _, _ := p.SourceStagePaused("source"); !paused {
		t.Fatal("source contract fault did not pause its stage")
	}
	if paused, _, _ := p.SourceStagePaused("reading"); paused {
		t.Fatal("source contract fault paused reading")
	}
	if !strings.Contains(logs.String(), `"event_name":"local_stage_paused"`) ||
		!strings.Contains(logs.String(), `"event_name":"claim_skipped_local_pause"`) ||
		strings.Contains(logs.String(), "bad source schema") {
		t.Fatalf("stage event is missing or exported a private error: %s", logs.String())
	}
	base.jobs = append(base.jobs,
		&cairn.Job{ID: 3, URL: url, LeaseToken: "source-3", Attempt: 1, SourceComponent: "source"})
	stats, err = p.RunSources(context.Background(), 2)
	if err != nil || stats.Claimed != 0 || len(base.jobs) != 1 || reader.fetches != 1 {
		t.Fatalf("paused source consumed a second task: %+v queued=%d fetches=%d err=%v",
			stats, len(base.jobs), reader.fetches, err)
	}
	if len(q.claimMasks) < 3 || q.claimMasks[len(q.claimMasks)-1] != [2]bool{false, true} {
		t.Fatalf("source pause did not filter before claim: %v", q.claimMasks)
	}
	now = now.Add(31 * time.Second)
	reader.fetchErr = nil
	stats, err = p.RunSources(context.Background(), 2)
	if err != nil || stats.Completed != 1 || reader.fetches != 2 {
		t.Fatalf("half-open source probe = %+v fetches=%d err=%v", stats, reader.fetches, err)
	}
	if paused, _, _ := p.SourceStagePaused("source"); paused {
		t.Fatal("successful source checkpoint did not close the local probe")
	}
	if !strings.Contains(logs.String(), `"event_name":"stage_probe_started"`) ||
		!strings.Contains(logs.String(), `"event_name":"stage_probe_succeeded"`) {
		t.Fatalf("probe lifecycle events are missing: %s", logs.String())
	}
}

func TestNewerLocalFaultCannotBeClearedByOldProbe(t *testing.T) {
	pause := newComponentPause()
	now := time.Now()
	pause.now = func() time.Time { return now }
	pause.tripStage("bad configuration")
	now = now.Add(31 * time.Second)
	allowed, halfOpen, epoch, _ := pause.beginStageProbe()
	if !allowed || !halfOpen {
		t.Fatal("stage did not offer a bounded half-open probe")
	}
	pause.tripStage("newer contract fault")
	probe := &sourceStageProbe{stage: "source", epoch: epoch, gate: pause}
	probe.succeed("source")
	if probe.outcome != "stage_probe_superseded" {
		t.Fatalf("old probe outcome = %q", probe.outcome)
	}
	if paused, reason, _ := pause.state(); !paused || reason != "newer contract fault" {
		t.Fatalf("old probe cleared newer fault: paused=%t reason=%q", paused, reason)
	}
}

func TestLocalConfigurationPauseSurvivesFailureReportOutage(t *testing.T) {
	job := &cairn.Job{ID: 7, URL: "https://x.com/u/status/7", LeaseToken: "lease-7",
		Attempt: 1, SourceComponent: "source"}
	q := &stageQueue{fakeQueue: newFakeQueue(job)}
	q.failErr = errors.New("worker failure report unavailable")
	reader := &stageReader{q: q, fetchErr: enrich.Classified(errors.New("private provider response"),
		enrich.ErrorClassConfiguration)}
	p := NewStaged(q, reader, nil, "", "", discardLogger(), 1)
	if err := p.Process(context.Background(), job); err == nil {
		t.Fatal("failure report outage was hidden")
	}
	if paused, _, _ := p.SourceStagePaused("source"); !paused {
		t.Fatal("local configuration pause was lost with the failure report")
	}
}

func TestRecoveredWorkerPanicReleasesLocalStageProbe(t *testing.T) {
	base := &stageQueue{fakeQueue: newFakeQueue(&cairn.Job{ID: 9,
		URL: "https://x.com/u/status/panic", LeaseToken: "source-9", Attempt: 1,
		SourceComponent: "source"})}
	q := &localPauseQueue{stageQueue: base}
	var logs bytes.Buffer
	p := NewStaged(q, &panickingSourceReader{&stageReader{q: base}}, nil, "", "",
		slog.New(observability.SafeJSONHandler(&logs, slog.LevelDebug)), 1)
	now := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	p.stages.sourcePause.now = func() time.Time { return now }
	p.stages.sourcePause.tripStage("original fault")
	now = now.Add(31 * time.Second)
	stats, err := p.RunSources(context.Background(), 1)
	if err == nil || stats.Claimed != 1 {
		t.Fatalf("recovered panic = %+v, %v", stats, err)
	}
	if paused, reason, _ := p.SourceStagePaused("source"); !paused || reason != "source stage probe interrupted" {
		t.Fatalf("probe after panic = paused=%t reason=%q", paused, reason)
	}
	if p.stages.sourcePause.probing {
		t.Fatal("recovered worker stranded the half-open stage probe")
	}
	if !strings.Contains(logs.String(), `"event_name":"stage_probe_failed"`) {
		t.Fatalf("recovered panic has no failed probe event: %s", logs.String())
	}
}

func TestLocalReadingContractPauseLetsSourceCheckpointContinue(t *testing.T) {
	const url = "https://x.com/u/status/reading"
	base := &stageQueue{fakeQueue: newFakeQueue(
		&cairn.Job{ID: 1, URL: url, LeaseToken: "reading-1", Attempt: 1, SourceComponent: "reading"},
		&cairn.Job{ID: 2, URL: url, LeaseToken: "source-2", Attempt: 1, SourceComponent: "source"})}
	base.source = &enrich.Source{OriginalText: "stored text", RelatedLinks: []string{}}
	q := &localPauseQueue{stageQueue: base, storedID: 1, storedSource: base.source}
	providerFault := enrich.Classified(errors.New("bad reading schema"), enrich.ErrorClassContract)
	reader := &stageReader{q: base, transformErr: providerFault}
	p := NewStaged(q, reader, nil, "", "", discardLogger(), 1)
	now := time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)
	p.stages.readingPause.now = func() time.Time { return now }
	stats, err := p.RunSources(context.Background(), 3)
	if err != nil || stats.Claimed != 2 || stats.Failed != 1 ||
		reader.fetches != 1 || reader.transforms != 1 || len(base.deferredStages) != 1 ||
		base.deferredStages[0] != "reading" {
		t.Fatalf("reading pause blocked source checkpoint: %+v fetches=%d reading=%d deferred=%v err=%v",
			stats, reader.fetches, reader.transforms, base.deferredStages, err)
	}
	if paused, _, _ := p.SourceStagePaused("source"); paused {
		t.Fatal("reading contract fault paused source retrieval")
	}
	if paused, _, _ := p.SourceStagePaused("reading"); !paused {
		t.Fatal("reading contract fault did not pause reading")
	}
	q.storedID, q.storedSource = 2, base.source
	base.jobs = append(base.jobs,
		&cairn.Job{ID: 2, URL: url, LeaseToken: "reading-2", Attempt: 2, SourceComponent: "reading"})
	stats, err = p.RunSources(context.Background(), 2)
	if err != nil || stats.Claimed != 0 || reader.transforms != 1 {
		t.Fatalf("paused reading was re-claimed: %+v reading=%d err=%v", stats, reader.transforms, err)
	}
	now = now.Add(31 * time.Second)
	reader.transformErr = nil
	stats, err = p.RunSources(context.Background(), 2)
	if err != nil || stats.Completed != 1 || reader.transforms != 2 {
		t.Fatalf("reading probe = %+v reading=%d err=%v", stats, reader.transforms, err)
	}
	if paused, _, _ := p.SourceStagePaused("reading"); paused {
		t.Fatal("successful reading completion did not close the local probe")
	}
}
