package health

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

//go:embed index.html
var dashboardHTML []byte

// Snapshot is the public, secret-free service status payload.
type Snapshot struct {
	Ready       bool             `json:"ready"`
	StartedAt   time.Time        `json:"started_at"`
	LastRunAt   *time.Time       `json:"last_run_at,omitempty"`
	LastSuccess *time.Time       `json:"last_success_at,omitempty"`
	LastError   string           `json:"last_error,omitempty"`
	LastStats   *processor.Stats `json:"last_stats,omitempty"`
	Build       buildinfo.Info   `json:"build"`
}

// Tracker stores thread-safe health and latest-batch state.
type Tracker struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

// NewTracker creates a ready tracker stamped with current build information.
func NewTracker() *Tracker {
	return &Tracker{snapshot: Snapshot{
		Ready:     true,
		StartedAt: time.Now().UTC(),
		Build:     buildinfo.Current(),
	}}
}

// Record updates the latest batch outcome.
func (t *Tracker) Record(stats processor.Stats, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UTC()
	t.snapshot.LastRunAt = &now
	t.snapshot.LastStats = &stats
	if err != nil || stats.Failed > 0 {
		if err != nil {
			t.snapshot.LastError = err.Error()
		} else {
			t.snapshot.LastError = "one or more enrichment jobs failed"
		}
		return
	}
	t.snapshot.LastError = ""
	t.snapshot.LastSuccess = &now
}

// Snapshot returns a point-in-time copy of tracker state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot
}

// Handler serves liveness, readiness, and latest-batch status endpoints.
func (t *Tracker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(dashboardHTML)
	})
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
		snapshot := t.Snapshot()
		status := http.StatusOK
		if !snapshot.Ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(writer, status, map[string]bool{"ready": snapshot.Ready})
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
