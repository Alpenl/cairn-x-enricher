package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// LogExporterStatus reports bounded exporter pressure without logging through
// the exporter itself. A blocked stdout or collector must not block a job.
type LogExporterStatus struct {
	QueueDepth    int    `json:"queue_depth"`
	QueueCapacity int    `json:"queue_capacity"`
	Dropped       uint64 `json:"dropped"`
	WriteErrors   uint64 `json:"write_errors"`
	Closed        bool   `json:"closed"`
}

type logEntry struct {
	handler slog.Handler
	record  slog.Record
}

type asyncLogState struct {
	mu          sync.RWMutex
	queue       chan logEntry
	done        chan struct{}
	closed      bool
	dropped     atomic.Uint64
	writeErrors atomic.Uint64
}

func (s *asyncLogState) status() LogExporterStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return LogExporterStatus{QueueDepth: len(s.queue), QueueCapacity: cap(s.queue),
		Dropped: s.dropped.Load(), WriteErrors: s.writeErrors.Load(), Closed: s.closed}
}

func (s *asyncLogState) close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// asyncLogHandler copies the record into a bounded queue before JSON encoding
// or writing. Child handlers keep their own attributes and groups while sharing
// one writer goroutine and pressure counters.
type asyncLogHandler struct {
	state *asyncLogState
	next  slog.Handler
}

func (h *asyncLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.state.mu.RLock()
	closed := h.state.closed
	h.state.mu.RUnlock()
	return !closed && h.next.Enabled(ctx, level)
}

func (h *asyncLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.state.mu.RLock()
	defer h.state.mu.RUnlock()
	if h.state.closed {
		return nil
	}
	clean := safeLogRecord(record)
	select {
	case h.state.queue <- logEntry{handler: h.next, record: clean}:
	default:
		h.state.dropped.Add(1)
	}
	return nil
}

func (h *asyncLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncLogHandler{state: h.state, next: h.next.WithAttrs(safeLogAttrs(attrs))}
}

func (h *asyncLogHandler) WithGroup(name string) slog.Handler {
	return &asyncLogHandler{state: h.state, next: h.next.WithGroup(safeLogGroup(name))}
}

// AsyncLogger installs one optional, bounded JSON exporter. Close drains queued
// records until its context expires. A writer that never returns can delay only
// the exporter goroutine, never the caller or shutdown past that deadline.
func (s *Store) AsyncLogger(writer io.Writer, capacity int) (*slog.Logger, func(context.Context) error, error) {
	if writer == nil || capacity < 1 || capacity > 16_384 {
		return nil, nil, errors.New("invalid log exporter configuration")
	}
	state := &asyncLogState{queue: make(chan logEntry, capacity), done: make(chan struct{})}
	if !s.export.CompareAndSwap(nil, state) {
		return nil, nil, errors.New("log exporter already installed")
	}
	go func() {
		defer close(state.done)
		for entry := range state.queue {
			if err := entry.handler.Handle(context.Background(), entry.record); err != nil {
				state.writeErrors.Add(1)
			}
		}
	}()
	base := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug}).WithAttrs(logIdentity)
	logger := slog.New(&logGate{store: s, next: &asyncLogHandler{state: state, next: base}})
	return logger, state.close, nil
}
