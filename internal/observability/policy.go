package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// LogMode controls the local JSON log signal. Other signals get their own
// settings when their exporters are installed; this mode never implies that
// metrics or traces are collecting data.
type LogMode string

// Log modes are independent of any future metric or trace exporters.
const (
	LogOff        LogMode = "off"
	LogBasic      LogMode = "basic"
	LogDiagnostic LogMode = "diagnostic"
	MaxDiagnostic         = time.Hour
)

// Configuration errors let the control endpoint distinguish stale or overly
// frequent writes from failures to persist a valid policy.
var (
	ErrVersionConflict  = errors.New("observability configuration version conflict")
	ErrRateLimited      = errors.New("observability configuration update rate limited")
	ErrInvalidPolicy    = errors.New("invalid observability configuration")
	ErrAuditUnavailable = errors.New("observability control audit unavailable")
)

// Policy is the persisted desired configuration. An expired diagnostic mode
// remains visible here for audit, while Effective reports the safe fallback.
type Policy struct {
	Version         uint64    `json:"version"`
	Logs            LogMode   `json:"logs"`
	FallbackLogs    LogMode   `json:"fallback_logs,omitempty"`
	DiagnosticUntil time.Time `json:"diagnostic_until,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
}

// Status distinguishes the persisted intent from the mode actually applied
// in this process. The same version is retained when a timer expires.
type Status struct {
	Desired                Policy             `json:"desired"`
	EffectiveLogs          LogMode            `json:"effective_logs"`
	AppliedVersion         uint64             `json:"applied_version"`
	MetricsAvailable       bool               `json:"metrics_available"`
	TracesAvailable        bool               `json:"traces_available"`
	LogExporter            *LogExporterStatus `json:"log_exporter,omitempty"`
	ControlAuditErrors     uint64             `json:"control_audit_errors"`
	ControlAuditAvailable  bool               `json:"control_audit_available"`
	WorkerPersistedVersion *uint64            `json:"worker_persisted_version,omitempty"`
	WorkerPublishState     string             `json:"worker_publish_state"`
	WorkerLastConfirmedAt  *time.Time         `json:"worker_last_confirmed_at,omitempty"`
}

type workerPublishState struct {
	version     *uint64
	state       string
	confirmedAt time.Time
}

// Store owns the local log switch. Updates are persisted before publication,
// so a failed write cannot make the running process disagree with the file.
type Store struct {
	mu          sync.Mutex
	path        string
	baseLevel   slog.Level
	current     atomic.Pointer[Policy]
	now         func() time.Time
	lastUpdate  time.Time
	export      atomic.Pointer[asyncLogState]
	audit       controlAudit
	auditReady  bool
	auditErrors atomic.Uint64
	worker      atomic.Pointer[workerPublishState]
}

// Open loads a previously saved policy or starts in basic mode. The directory
// must already exist; a read-only container mounts a dedicated writable volume.
func Open(path string, baseLevel slog.Level) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("observability policy path must be absolute")
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("observability policy directory is unavailable: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("observability policy parent is not a directory")
	}
	probe, err := os.CreateTemp(dir, ".observability-probe-*")
	if err != nil {
		return nil, fmt.Errorf("observability policy directory is not writable: %w", err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	s := &Store{path: path, baseLevel: baseLevel, now: time.Now}
	policy := Policy{Logs: LogBasic}
	//nolint:gosec // This path is a trusted, validated local configuration path.
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 4096 {
			return nil, errors.New("observability policy is too large")
		}
		fileInfo, statErr := os.Stat(path)
		if statErr != nil || fileInfo.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("observability policy file must be private")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&policy); err != nil {
			return nil, fmt.Errorf("decode observability policy: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("observability policy has trailing data")
		}
		if err := validateSaved(policy); err != nil {
			return nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, err := s.persist(policy); err != nil {
			return nil, fmt.Errorf("initialize observability policy: %w", err)
		}
	} else {
		return nil, fmt.Errorf("read observability policy: %w", err)
	}
	s.current.Store(&policy)
	s.auditReady = true
	if err := s.openAudit(); err != nil {
		// A corrupt optional audit must not prevent ordinary processing. Keep
		// the control plane read-only until an operator repairs it and restarts.
		s.auditReady = false
		if s.auditErrors.Load() == 0 {
			s.auditErrors.Add(1)
		}
	}
	return s, nil
}

func validateSaved(policy Policy) error {
	switch policy.Logs {
	case LogOff, LogBasic:
		if !policy.DiagnosticUntil.IsZero() || policy.FallbackLogs != "" {
			return errors.New("non-diagnostic log mode has a fallback or expiry")
		}
	case LogDiagnostic:
		if policy.DiagnosticUntil.IsZero() || policy.UpdatedAt.IsZero() ||
			policy.DiagnosticUntil.Sub(policy.UpdatedAt) <= 0 || policy.DiagnosticUntil.Sub(policy.UpdatedAt) > MaxDiagnostic ||
			(policy.FallbackLogs != LogOff && policy.FallbackLogs != LogBasic) {
			return errors.New("diagnostic log mode requires a bounded expiry")
		}
	default:
		return errors.New("invalid log mode")
	}
	return nil
}

// Snapshot is safe to call while an update is being persisted. An expired
// diagnostic period immediately falls back to its prior mode without another write.
func (s *Store) Snapshot() Status {
	desired := *s.current.Load()
	status := Status{Desired: desired, EffectiveLogs: s.effectiveLogs(&desired), AppliedVersion: desired.Version,
		ControlAuditErrors: s.auditErrors.Load(), ControlAuditAvailable: s.auditReady,
		WorkerPublishState: "pending"}
	if published := s.worker.Load(); published != nil {
		status.WorkerPersistedVersion = published.version
		status.WorkerPublishState = published.state
		if !published.confirmedAt.IsZero() {
			confirmedAt := published.confirmedAt
			status.WorkerLastConfirmedAt = &confirmedAt
		}
		if published.version != nil && *published.version < desired.Version && published.state == "confirmed" {
			status.WorkerPublishState = "pending"
		}
	}
	if exporter := s.export.Load(); exporter != nil {
		stats := exporter.status()
		status.LogExporter = &stats
	}
	return status
}

// SetWorkerPublishResult records only safe control status. A confirmed version
// means durable acceptance by one Worker request, not all isolates refreshed.
func (s *Store) SetWorkerPublishResult(version *uint64, state string) {
	previous := s.worker.Load()
	next := &workerPublishState{state: state}
	if previous != nil {
		next.version = previous.version
		next.confirmedAt = previous.confirmedAt
	}
	if version != nil {
		confirmed := *version
		next.version = &confirmed
		next.confirmedAt = time.Now().UTC()
		next.state = "confirmed"
	}
	s.worker.Store(next)
}

func (s *Store) effectiveLogs(policy *Policy) LogMode {
	if policy.Logs == LogDiagnostic && !s.now().Before(policy.DiagnosticUntil) {
		return policy.FallbackLogs
	}
	return policy.Logs
}

// Update requires compare-and-swap on the persisted version. Detailed logging
// has a finite lifetime, even if the process restarts while it is enabled.
func (s *Store) Update(expected uint64, mode LogMode, ttl time.Duration) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditReady {
		return s.Snapshot(), ErrAuditUnavailable
	}
	previous := s.current.Load()
	if previous.Version != expected {
		return Status{}, ErrVersionConflict
	}
	if previous.Version == ^uint64(0) {
		return Status{}, ErrInvalidPolicy
	}
	nowRaw := s.now()
	now := nowRaw.UTC()
	if !s.lastUpdate.IsZero() && nowRaw.Sub(s.lastUpdate) < time.Second {
		return Status{}, ErrRateLimited
	}
	policy := Policy{Version: previous.Version + 1, Logs: mode, UpdatedAt: now, UpdatedBy: "container_loopback"}
	switch mode {
	case LogDiagnostic:
		if ttl <= 0 || ttl > MaxDiagnostic {
			return Status{}, fmt.Errorf("%w: diagnostic duration must be within %s", ErrInvalidPolicy, MaxDiagnostic)
		}
		policy.FallbackLogs = previous.Logs
		if previous.Logs == LogDiagnostic {
			policy.FallbackLogs = previous.FallbackLogs
		}
		policy.DiagnosticUntil = now.Add(ttl)
	case LogBasic, LogOff:
		if ttl != 0 {
			return Status{}, fmt.Errorf("%w: duration is only allowed for diagnostic logging", ErrInvalidPolicy)
		}
	default:
		return Status{}, fmt.Errorf("%w: invalid log mode", ErrInvalidPolicy)
	}
	if err := s.appendAudit(ControlAuditEntry{At: now, Version: policy.Version,
		ExpectedVersion: expected, Signal: "logs", Mode: mode, DiagnosticUntil: policy.DiagnosticUntil,
		Actor: "container_loopback", Result: "started"}); err != nil {
		return s.Snapshot(), fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	committed, err := s.persist(policy)
	if committed {
		s.current.Store(&policy)
		s.lastUpdate = nowRaw
	}
	result := "rejected"
	if committed {
		result = "applied"
		if err != nil {
			result = "unconfirmed"
		}
	}
	auditErr := s.appendAudit(ControlAuditEntry{At: now, Version: policy.Version,
		ExpectedVersion: expected, Signal: "logs", Mode: mode, DiagnosticUntil: policy.DiagnosticUntil,
		Actor: "container_loopback", Result: result})
	return s.Snapshot(), errors.Join(err, auditErr)
}

func (s *Store) persist(policy Policy) (bool, error) {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".observability-*")
	if err != nil {
		return false, fmt.Errorf("create observability policy: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := json.NewEncoder(tmp).Encode(policy); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return false, fmt.Errorf("publish observability policy: %w", err)
	}
	//nolint:gosec // The parent of the trusted policy path is synced after rename.
	handle, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer func() { _ = handle.Close() }()
	return true, handle.Sync()
}

// Logger is a direct-writer helper for policy tests. It has no bounded queue
// or private-field filter; the serve command uses AsyncLogger instead.
func (s *Store) Logger(writer io.Writer) *slog.Logger {
	base := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(&logGate{store: s, next: base})
}

type logGate struct {
	store *Store
	next  slog.Handler
}

func (h *logGate) Enabled(ctx context.Context, level slog.Level) bool {
	mode := h.store.effectiveLogs(h.store.current.Load())
	if mode == LogOff {
		return false
	}
	if mode == LogDiagnostic {
		return level >= slog.LevelDebug && h.next.Enabled(ctx, level)
	}
	return level >= max(h.store.baseLevel, slog.LevelInfo) && h.next.Enabled(ctx, level)
}

func (h *logGate) Handle(ctx context.Context, record slog.Record) error {
	if !h.Enabled(ctx, record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record)
}

func (h *logGate) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logGate{store: h.store, next: h.next.WithAttrs(attrs)}
}

func (h *logGate) WithGroup(name string) slog.Handler {
	return &logGate{store: h.store, next: h.next.WithGroup(name)}
}
