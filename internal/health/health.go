package health

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// Clock allows tests to control staleness evaluation.
type Clock func() time.Time

// Snapshot is the public, secret-free service status payload.
type Snapshot struct {
	Ready          bool             `json:"ready"`
	ReadyReason    string           `json:"ready_reason,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	LastRunAt      *time.Time       `json:"last_run_at,omitempty"`
	LastSuccess    *time.Time       `json:"last_success_at,omitempty"`
	LastWorkAt     *time.Time       `json:"last_work_at,omitempty"`
	LastError      string           `json:"last_error,omitempty"`
	LastStats      *processor.Stats `json:"last_stats,omitempty"`
	LastWorkStats  *processor.Stats `json:"last_work_stats,omitempty"`
	UnhealthySince *time.Time       `json:"unhealthy_since,omitempty"`
	Build          buildinfo.Info   `json:"build"`
}

// Tracker stores thread-safe health and latest-batch state.
type Tracker struct {
	mu       sync.RWMutex
	snapshot Snapshot
	clock    Clock

	// degraded marks failures that require an explicit recovery signal
	// rather than merely a later successful batch. It covers vendor and
	// configuration faults the process cannot fix by retrying.
	degraded     bool
	degradedErr  string
	lastRecovery time.Time
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
	t.degraded = false
	t.degradedErr = ""
	t.lastRecovery = t.clock()
	t.refreshReadyLocked()
}

// MarkDegraded flags a fault the process cannot repair by retrying alone,
// such as an upstream contract violation or repeated authentication failure.
// Readiness stays false until a successful recovery check clears it.
func (t *Tracker) MarkDegraded(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if reason == "" {
		reason = "degraded"
	}
	t.degraded = true
	t.degradedErr = reason
	t.refreshReadyLocked()
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
	switch {
	case t.degraded:
		reason = t.degradedErr
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
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot.Ready
}

// Snapshot returns a point-in-time copy of tracker state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot
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
