package health

import (
	"strings"
	"testing"
	"time"
)

func TestLaneHeartbeatExpiresAndRecoversWithBoundedBusyGrace(t *testing.T) {
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	tracker := NewTrackerWithClock(func() time.Time { return now })
	tracker.MarkStarted()
	tracker.PulseLane("classification", "running", 5*time.Minute)
	now = now.Add(4 * time.Minute)
	if !tracker.Ready() {
		t.Fatal("legitimate bounded work was marked stale")
	}
	now = now.Add(2 * time.Minute)
	if tracker.Ready() || !strings.Contains(tracker.Snapshot().ReadyReason, "heartbeat expired") {
		t.Fatal("stalled lane remained ready")
	}
	tracker.PulseLane("classification", "waiting", time.Minute)
	if !tracker.Ready() {
		t.Fatal("fresh heartbeat did not recover lane")
	}
	snapshot := tracker.Snapshot()
	delete(snapshot.Lanes, "classification")
	if len(tracker.Snapshot().Lanes) != 1 {
		t.Fatal("snapshot exposed mutable lane map")
	}
}

func TestRestartingLaneStaysDegradedUntilRoundCompletes(t *testing.T) {
	tracker := NewTracker()
	tracker.MarkStarted()
	tracker.FailLane("source", "restarting", 1)
	tracker.PulseLane("source", "starting", time.Minute)
	if tracker.Ready() {
		t.Fatal("restart launch fabricated recovery")
	}
	tracker.PulseLane("source", "waiting", time.Minute)
	if !tracker.Ready() {
		t.Fatal("completed round did not clear supervisor fault")
	}
}

func TestParallelProgressCannotMaskOneExpiredTask(t *testing.T) {
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	tracker := NewTrackerWithClock(func() time.Time { return now })
	tracker.MarkStarted()
	tracker.PulseLane("source", "running", time.Minute)
	stalled := tracker.BeginLaneWork("source", 15*time.Minute)
	for range 100 {
		now = now.Add(10 * time.Second)
		finish := tracker.BeginLaneWork("source", 15*time.Minute)
		finish()
	}
	if tracker.Ready() || !strings.Contains(tracker.Snapshot().ReadyReason, "heartbeat expired: source") {
		t.Fatal("one hundred healthy parallel completions concealed a stalled task")
	}
	if tracker.Snapshot().Lanes["source"].ActiveWork != 1 {
		t.Fatal("completed tasks were not removed")
	}
	stalled()
	stalled()
	if !tracker.Ready() || tracker.Snapshot().Lanes["source"].ActiveWork != 0 {
		t.Fatal("real completion did not recover, or completion was not idempotent")
	}
}
