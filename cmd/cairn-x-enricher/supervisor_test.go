package main

import (
	"context"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

func TestSupervisorRestartsPanicWithFiniteBackoffAndVisibleFailure(t *testing.T) {
	tracker := health.NewTracker()
	tracker.MarkStarted()
	var waits []time.Duration
	policy := restartPolicy{3, time.Second, 30 * time.Second, time.Hour, func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }}
	calls := 0
	superviseLane(context.Background(), tracker, discardLogger(), "source", func(context.Context) { calls++; panic("fixture private payload") }, policy)
	state := tracker.Snapshot()
	if calls != 4 || len(waits) != 3 || waits[0] != time.Second || waits[2] != 4*time.Second || state.Ready || state.Lanes["source"].State != "failed" {
		t.Fatalf("unsupervised failure: calls=%d waits=%v state=%+v", calls, waits, state)
	}
}

func TestSupervisorCancellationNeverRestartsAndSuccessfulRoundRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker := health.NewTracker()
	tracker.MarkStarted()
	calls := 0
	policy := defaultRestartPolicy()
	policy.wait = func(context.Context, time.Duration) bool { return true }
	superviseLane(ctx, tracker, discardLogger(), "classification", func(context.Context) {
		calls++
		if calls == 1 {
			panic("adapter fault")
		}
		tracker.PulseLane("classification", "waiting", time.Minute)
		cancel()
	}, policy)
	if calls != 2 || !tracker.Ready() {
		t.Fatalf("recovery or cancellation failed: calls=%d state=%+v", calls, tracker.Snapshot())
	}
}

func TestCriticalServerPanicProducesExitResult(t *testing.T) {
	if err := criticalServerResult(func() error { panic("private panic value") }); err == nil || err.Error() != "critical server task panicked" {
		t.Fatalf("silent or unsafe server failure: %v", err)
	}
}

func TestPeriodicLaneFailureRestartsAndWorkMustActuallyComplete(t *testing.T) {
	for _, name := range []string{"policy_publisher", "audit_pruning"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tracker := health.NewTracker()
			tracker.MarkStarted()
			calls := 0
			policy := defaultRestartPolicy()
			policy.wait = func(context.Context, time.Duration) bool { return true }
			superviseLane(ctx, tracker, discardLogger(), name, func(ctx context.Context) {
				runMonitoredPeriodic(ctx, tracker, name, time.Hour, time.Minute, func() {
					calls++
					if calls == 1 {
						panic("private maintenance failure")
					}
					if tracker.Ready() {
						t.Fatal("launching a failed task fabricated recovery")
					}
					cancel()
				})
			}, policy)
			if calls != 2 || !tracker.Ready() {
				t.Fatalf("maintenance loop did not recover: %d %+v", calls, tracker.Snapshot())
			}
		})
	}
}
