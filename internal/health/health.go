package health

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// Clock allows tests to control staleness evaluation.
type Clock func() time.Time

// Snapshot is the public, secret-free service status payload.
type Snapshot struct {
	Ready                   bool                  `json:"ready"`
	ReadyReason             string                `json:"ready_reason,omitempty"`
	StartedAt               time.Time             `json:"started_at"`
	LastRunAt               *time.Time            `json:"last_run_at,omitempty"`
	LastSuccess             *time.Time            `json:"last_success_at,omitempty"`
	LastWorkAt              *time.Time            `json:"last_work_at,omitempty"`
	LastError               string                `json:"last_error,omitempty"`
	LastStats               *processor.Stats      `json:"last_stats,omitempty"`
	LastWorkStats           *processor.Stats      `json:"last_work_stats,omitempty"`
	LastClassificationRunAt *time.Time            `json:"last_classification_run_at,omitempty"`
	LastClassificationStats *processor.Stats      `json:"last_classification_stats,omitempty"`
	LastClassificationError string                `json:"last_classification_error,omitempty"`
	UnhealthySince          *time.Time            `json:"unhealthy_since,omitempty"`
	Build                   buildinfo.Info        `json:"build"`
	Lanes                   map[string]LaneStatus `json:"lanes,omitempty"`
	DegradedComponents      map[string]string     `json:"degraded_components,omitempty"`
}

// LaneStatus exposes fixed lane names and bounded status, never panic values or
// source material. Grace accounts for one bounded task or a polling interval.
type LaneStatus struct {
	State         string        `json:"state"`
	LastHeartbeat time.Time     `json:"last_heartbeat"`
	Grace         time.Duration `json:"grace_ns"`
	Restarts      int           `json:"restarts"`
	ActiveWork    int           `json:"active_work,omitempty"`
}

// PulseLane reports real scheduling progress or the start of a bounded wait.
func (t *Tracker) PulseLane(name, state string, grace time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snapshot.Lanes == nil {
		t.snapshot.Lanes = map[string]LaneStatus{}
	}
	lane := t.snapshot.Lanes[name]
	lane.State, lane.LastHeartbeat, lane.Grace = state, t.clock(), max(grace, time.Second)
	t.snapshot.Lanes[name] = lane
	if state == "waiting" {
		delete(t.degraded, "scheduler_"+name)
	}
	t.refreshReadyLocked()
}

// FailLane exposes a failed or restarting scheduler without fabricating recovery.
func (t *Tracker) FailLane(name, state string, restarts int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snapshot.Lanes == nil {
		t.snapshot.Lanes = map[string]LaneStatus{}
	}
	lane := t.snapshot.Lanes[name]
	lane.State, lane.Restarts, lane.LastHeartbeat = state, restarts, t.clock()
	t.snapshot.Lanes[name] = lane
	if t.degraded == nil {
		t.degraded = map[string]string{}
	}
	t.degraded["scheduler_"+name] = name + " scheduler " + state
	t.refreshReadyLocked()
}

// BeginLaneWork watches one activity independently of parallel task progress.
// The returned completion is idempotent, and no background timer refreshes it.
func (t *Tracker) BeginLaneWork(name string, grace time.Duration) func() {
	t.mu.Lock()
	if t.snapshot.Lanes == nil {
		t.snapshot.Lanes = make(map[string]LaneStatus)
	}
	if t.active == nil {
		t.active = make(map[string]map[uint64]time.Time)
	}
	if t.active[name] == nil {
		t.active[name] = make(map[uint64]time.Time)
	}
	t.sequence++
	key := t.sequence
	t.active[name][key] = t.clock().Add(max(grace, time.Second))
	lane := t.snapshot.Lanes[name]
	lane.LastHeartbeat, lane.ActiveWork = t.clock(), len(t.active[name])
	t.snapshot.Lanes[name] = lane
	t.refreshReadyLocked()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			delete(t.active[name], key)
			lane := t.snapshot.Lanes[name]
			lane.LastHeartbeat, lane.ActiveWork = t.clock(), len(t.active[name])
			t.snapshot.Lanes[name] = lane
			t.refreshReadyLocked()
		})
	}
}

// Tracker stores thread-safe health and latest-batch state.
type Tracker struct {
	mu       sync.RWMutex
	snapshot Snapshot
	clock    Clock

	// degraded marks failures that require an explicit recovery signal
	// rather than merely a later successful batch. It covers vendor and
	// configuration faults the process cannot fix by retrying.
	degraded     map[string]string
	lastRecovery time.Time
	active       map[string]map[uint64]time.Time
	sequence     uint64
}

// NewTracker creates a tracker that starts unready until the first
// successful reconcile confirms the process can actually serve work.
func NewTracker() *Tracker {
	return NewTrackerWithClock(time.Now)
}

// NewTrackerWithClock is NewTracker with an injectable clock for tests.
func NewTrackerWithClock(clock Clock) *Tracker {
	now := clock()
	return &Tracker{
		clock: clock,
		snapshot: Snapshot{
			Ready:          false,
			ReadyReason:    "starting",
			StartedAt:      now.UTC(),
			UnhealthySince: &now,
			Build:          buildinfo.Current(),
		},
	}
}

// MarkStarted is the explicit readiness gate used when a component becomes
// able to serve work. It clears any degraded state, because reaching this
// point proves the previously failing dependency is reachable again.
func (t *Tracker) MarkStarted() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.degraded = nil
	t.lastRecovery = t.clock()
	t.refreshReadyLocked()
}

// MarkDegraded flags a fault the process cannot repair by retrying alone,
// such as an upstream contract violation or repeated authentication failure.
// Readiness stays false until a successful recovery check clears it.
func (t *Tracker) MarkDegraded(reason string) {
	t.MarkComponentDegraded("general", reason)
}

// MarkComponentDegraded keeps faults independent across concurrent schedulers.
func (t *Tracker) MarkComponentDegraded(component, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reason == "" {
		reason = "degraded"
	}
	if t.degraded == nil {
		t.degraded = make(map[string]string)
	}
	t.degraded[component] = reason
	t.refreshReadyLocked()
}

// MarkComponentRecovered clears only the component proven to have recovered.
func (t *Tracker) MarkComponentRecovered(component string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.degraded, component)
	t.refreshReadyLocked()
}

// RecordClassification reports a semantic round without overwriting source
// batch statistics in /status.
func (t *Tracker) RecordClassification(stats processor.Stats, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	t.snapshot.LastClassificationRunAt = &now
	t.snapshot.LastClassificationStats = &stats
	if err != nil {
		t.snapshot.LastClassificationError = err.Error()
	} else if stats.ClassificationFailed > 0 {
		t.snapshot.LastClassificationError = "one or more classification jobs failed"
	} else {
		t.snapshot.LastClassificationError = ""
	}
}

// Record updates the latest batch outcome.
func (t *Tracker) Record(stats processor.Stats, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	t.snapshot.LastRunAt = &now
	t.snapshot.LastStats = &stats
	if stats.HasWork() {
		t.snapshot.LastWorkAt = &now
		t.snapshot.LastWorkStats = &stats
	}
	if err != nil || stats.Failed > 0 || stats.ClassificationFailed > 0 {
		if err != nil {
			t.snapshot.LastError = err.Error()
		} else if stats.ClassificationFailed > 0 {
			t.snapshot.LastError = "one or more classification jobs failed"
		} else {
			t.snapshot.LastError = "one or more enrichment jobs failed"
		}
		t.refreshReadyLocked()
		return
	}
	t.snapshot.LastError = ""
	t.snapshot.LastSuccess = &now
	t.refreshReadyLocked()
}

// refreshReadyLocked recomputes readiness from the current fault state.
// Callers must hold t.mu.
func (t *Tracker) refreshReadyLocked() {
	reason := ""
	stale := []string{}
	for name, lane := range t.snapshot.Lanes {
		expired := false
		for _, deadline := range t.active[name] {
			if t.clock().After(deadline) {
				expired = true
				break
			}
		}
		if lane.State != "failed" && (expired || len(t.active[name]) == 0 && t.clock().Sub(lane.LastHeartbeat) > lane.Grace) {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	switch {
	case len(t.degraded) > 0:
		keys := make([]string, 0, len(t.degraded))
		for key := range t.degraded {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		reasons := make([]string, 0, len(keys))
		for _, key := range keys {
			reasons = append(reasons, t.degraded[key])
		}
		reason = strings.Join(reasons, "; ")
	case len(stale) > 0:
		reason = "scheduler heartbeat expired: " + strings.Join(stale, ", ")
	case t.lastRecovery.IsZero():
		reason = "starting"
	}
	t.snapshot.Ready = reason == ""
	t.snapshot.ReadyReason = reason
	if t.snapshot.Ready {
		t.snapshot.UnhealthySince = nil
		return
	}
	if t.snapshot.UnhealthySince == nil {
		now := t.clock()
		t.snapshot.UnhealthySince = &now
	}
}

// Ready reports whether the service can currently process work.
func (t *Tracker) Ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshReadyLocked()
	return t.snapshot.Ready
}

// Snapshot returns a point-in-time copy of tracker state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshReadyLocked()
	snapshot := t.snapshot
	snapshot.Lanes = make(map[string]LaneStatus, len(t.snapshot.Lanes))
	for name, lane := range t.snapshot.Lanes {
		snapshot.Lanes[name] = lane
	}
	snapshot.DegradedComponents = make(map[string]string, len(t.degraded))
	for name, reason := range t.degraded {
		snapshot.DegradedComponents[name] = reason
	}
	return snapshot
}

// Handler serves liveness, readiness, and latest-batch status endpoints.
// Liveness never depends on upstream state: a process that cannot reach the
// Worker must stay up so it can recover instead of crash-looping.
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
		snapshot := t.Snapshot()
		status := http.StatusOK
		if !snapshot.Ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(writer, status, snapshot)
	})
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, t.Snapshot())
	})
	return mux
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
