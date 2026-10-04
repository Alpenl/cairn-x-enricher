package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestSourcePreflightKeepsClassifierIndependentAndRecoversWithoutClaiming(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), job: &cairn.ClassificationJob{ID: 5}}
	p := NewStaged(q, &stageReader{q: q}, stageClassifier{}, "", "", discardLogger(), 1)
	checks := 0
	p.SetSourcePreflight(func(context.Context) error { checks++; return nil }, errors.New("provider unavailable"))
	now := time.Now()
	p.stages.preflight.gate.now = func() time.Time { return now }
	if _, err := p.RunSources(context.Background(), 1); !errors.Is(err, ErrSourcePreflightPaused) || checks != 0 {
		t.Fatalf("paused source consumed admission: checks=%d err=%v", checks, err)
	}
	if done, _, err := p.RunClassifications(context.Background(), 1); err != nil || done != 1 {
		t.Fatalf("source outage blocked classification: done=%d err=%v", done, err)
	}
	now = now.Add(time.Minute)
	if _, err := p.RunSources(context.Background(), 1); err != nil || checks != 1 {
		t.Fatalf("recovery: checks=%d err=%v", checks, err)
	}
	if paused, _, _ := p.SourceStagePaused("reading"); paused {
		t.Fatal("successful preflight did not recover reading")
	}
}

func TestUnverifiedPreflightRunsOnceAndFailedProbeBacksOff(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	p := NewStaged(q, &stageReader{q: q}, nil, "", "", discardLogger(), 1)
	checks := 0
	p.SetSourcePreflight(func(context.Context) error { checks++; return errors.New("bad schema") }, ErrSourcePreflightUnverified)
	for range 2 {
		_, _ = p.RunSources(context.Background(), 1)
	}
	if checks != 1 {
		t.Fatalf("initial failure retried without backoff: %d", checks)
	}
	if paused, _, _ := p.SourceStagePaused("source"); !paused {
		t.Fatal("missing source failure state")
	}
}

func TestEvidenceWakeupOnlyAfterDurableSuccessAndCoalesces(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue(), evidenceErr: errors.New("worker offline")}
	p := NewStaged(q, nil, nil, "", "", discardLogger(), 1)
	wakeup := make(chan struct{}, 1)
	p.SetClassificationWakeup(wakeup)
	if err := p.persistSourceEvidence(context.Background(), 1, enrich.Source{OriginalText: "fixture"}); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if len(wakeup) != 0 {
		t.Fatal("failed checkpoint woke classifier")
	}
	q.evidenceErr = nil
	for range 2 {
		if err := p.persistSourceEvidence(context.Background(), 1, enrich.Source{OriginalText: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(wakeup) != 1 {
		t.Fatalf("durable notifications did not coalesce: %d", len(wakeup))
	}
}

func TestManualRecoveryRequiresAdmissionAndCoalescesWithActiveCheck(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	p := NewStaged(q, &stageReader{q: q}, nil, "", "", discardLogger(), 1)
	checks := 0
	p.SetSourcePreflight(func(context.Context) error { checks++; return nil }, errors.New("down"))
	accepted := false
	p.SetSourceRecovery(func(context.Context) (cairn.ProviderCheckStatus, error) {
		return cairn.ProviderCheckStatus{State: "waiting"}, nil
	},
		func(context.Context) (cairn.ProviderCheckStatus, error) {
			return cairn.ProviderCheckStatus{Accepted: accepted}, nil
		})
	_, _ = p.RecoverSource(context.Background())
	if err := p.checkSourcePreflight(context.Background()); !errors.Is(err, ErrSourcePreflightPaused) || checks != 0 {
		t.Fatalf("unadmitted recovery started check: %d %v", checks, err)
	}
	accepted = true
	_, _ = p.RecoverSource(context.Background())
	if err := p.checkSourcePreflight(context.Background()); err != nil || checks != 1 {
		t.Fatalf("admitted recovery: %d %v", checks, err)
	}
	if err := p.checkSourcePreflight(context.Background()); err != nil || checks != 1 {
		t.Fatalf("success was not reused: %d %v", checks, err)
	}
}
func TestRemotePreflightDeadlineSurvivesLocalBackoff(t *testing.T) {
	q := &stageQueue{fakeQueue: newFakeQueue()}
	p := NewStaged(q, &stageReader{q: q}, nil, "", "", discardLogger(), 1)
	deadline := time.Now().Add(time.Hour)
	p.SetSourcePreflight(func(context.Context) error { return &PreflightRetry{At: deadline} }, ErrSourcePreflightUnverified)
	_ = p.checkSourcePreflight(context.Background())
	_, _, remaining := p.SourceStagePaused("source")
	if remaining < 59*time.Minute {
		t.Fatalf("lost authoritative retry delay: %v", remaining)
	}
}
