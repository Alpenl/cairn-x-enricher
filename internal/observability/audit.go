package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	controlAuditSchemaVersion = 1
	controlAuditRetention     = 14 * 24 * time.Hour
	controlAuditMaxEntries    = 4096
	controlAuditMaxBytes      = 256 * 1024
)

// ControlAuditEntry records only a versioned control action. A started entry
// without a final result means the process may have stopped between the audit
// write and the policy write; the current policy must be inspected separately.
type ControlAuditEntry struct {
	At              time.Time `json:"at"`
	Version         uint64    `json:"version"`
	ExpectedVersion uint64    `json:"expected_version"`
	Signal          string    `json:"signal"`
	Mode            LogMode   `json:"mode"`
	DiagnosticUntil time.Time `json:"diagnostic_until,omitzero"`
	Actor           string    `json:"actor"`
	Result          string    `json:"result"`
}

type controlAudit struct {
	SchemaVersion          int                 `json:"schema_version"`
	TruncatedBeforeVersion uint64              `json:"truncated_before_version,omitempty"`
	Entries                []ControlAuditEntry `json:"entries"`
}

// ControlAudit is available only on the container-loopback control listener.
// The version gap reports when the bounded file no longer contains early work.
type ControlAudit struct {
	SchemaVersion          int                 `json:"schema_version"`
	TruncatedBeforeVersion uint64              `json:"truncated_before_version,omitempty"`
	Entries                []ControlAuditEntry `json:"entries"`
}

func (s *Store) auditPath() string { return s.path + ".audit.json" }

func (s *Store) openAudit() error {
	path := s.auditPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		initial := controlAudit{SchemaVersion: controlAuditSchemaVersion, Entries: []ControlAuditEntry{}}
		if _, err := s.persistAudit(initial); err != nil {
			return fmt.Errorf("initialize observability control audit: %w", err)
		}
		s.audit = initial
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat observability control audit: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > controlAuditMaxBytes {
		return errors.New("observability control audit must be a small private regular file")
	}
	//nolint:gosec // The trusted absolute path and private regular-file check are above.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read observability control audit: %w", err)
	}
	var audit controlAudit
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&audit); err != nil {
		return fmt.Errorf("decode observability control audit: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("observability control audit has trailing data")
	}
	if audit.SchemaVersion != controlAuditSchemaVersion || len(audit.Entries) > controlAuditMaxEntries {
		return errors.New("unsupported observability control audit")
	}
	var priorVersion uint64
	for _, entry := range audit.Entries {
		if entry.At.IsZero() || entry.Version == 0 || entry.Version < priorVersion ||
			entry.Signal != "logs" || entry.Actor != "container_loopback" ||
			(entry.Mode != LogOff && entry.Mode != LogBasic && entry.Mode != LogDiagnostic) ||
			(entry.Result != "started" && entry.Result != "applied" && entry.Result != "rejected" && entry.Result != "unconfirmed") ||
			(entry.Mode == LogDiagnostic && entry.DiagnosticUntil.IsZero()) ||
			(entry.Mode != LogDiagnostic && !entry.DiagnosticUntil.IsZero()) {
			return errors.New("invalid observability control audit entry")
		}
		priorVersion = entry.Version
	}
	s.audit = audit
	return s.PruneAudit()
}

func trimAudit(audit *controlAudit, now time.Time) bool {
	changed := false
	cutoff := now.Add(-controlAuditRetention)
	kept := audit.Entries[:0]
	for _, entry := range audit.Entries {
		if entry.At.Before(cutoff) {
			audit.TruncatedBeforeVersion = max(audit.TruncatedBeforeVersion, entry.Version)
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	audit.Entries = kept
	for len(audit.Entries) > controlAuditMaxEntries {
		audit.TruncatedBeforeVersion = max(audit.TruncatedBeforeVersion, audit.Entries[0].Version)
		audit.Entries = audit.Entries[1:]
		changed = true
	}
	return changed
}

func (s *Store) appendAudit(entry ControlAuditEntry) error {
	next := controlAudit{SchemaVersion: controlAuditSchemaVersion,
		TruncatedBeforeVersion: s.audit.TruncatedBeforeVersion,
		Entries:                append(append([]ControlAuditEntry(nil), s.audit.Entries...), entry)}
	trimAudit(&next, entry.At)
	fits := func(drop int) (bool, error) {
		candidate := next
		for _, removed := range next.Entries[:drop] {
			candidate.TruncatedBeforeVersion = max(candidate.TruncatedBeforeVersion, removed.Version)
		}
		candidate.Entries = next.Entries[drop:]
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return false, err
		}
		return len(encoded)+1 <= controlAuditMaxBytes, nil // Encoder adds one newline.
	}
	if ok, err := fits(0); err != nil {
		return err
	} else if !ok {
		low, high := 1, len(next.Entries)
		for low < high {
			mid := low + (high-low)/2
			ok, err := fits(mid)
			if err != nil {
				return err
			}
			if ok {
				high = mid
			} else {
				low = mid + 1
			}
		}
		if ok, err := fits(low); err != nil {
			return err
		} else if !ok || low == len(next.Entries) {
			return errors.New("observability control audit entry exceeds capacity")
		}
		for _, removed := range next.Entries[:low] {
			next.TruncatedBeforeVersion = max(next.TruncatedBeforeVersion, removed.Version)
		}
		next.Entries = next.Entries[low:]
	}
	committed, err := s.persistAudit(next)
	if committed {
		s.audit = next
	}
	if err != nil {
		s.auditErrors.Add(1)
	}
	return err
}

// PruneAudit removes expired control history even if application logs are off.
// The serve process calls it hourly; Open also calls it after a restart.
func (s *Store) PruneAudit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditReady {
		return ErrAuditUnavailable
	}
	next := controlAudit{SchemaVersion: controlAuditSchemaVersion,
		TruncatedBeforeVersion: s.audit.TruncatedBeforeVersion,
		Entries:                append([]ControlAuditEntry(nil), s.audit.Entries...)}
	if !trimAudit(&next, s.now().UTC()) {
		return nil
	}
	committed, err := s.persistAudit(next)
	if committed {
		s.audit = next
	}
	if err != nil {
		s.auditErrors.Add(1)
	}
	return err
}

// Audit returns a copy of the bounded control history for the local control API.
func (s *Store) Audit() ControlAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ControlAudit{SchemaVersion: controlAuditSchemaVersion,
		TruncatedBeforeVersion: s.audit.TruncatedBeforeVersion,
		Entries:                append([]ControlAuditEntry(nil), s.audit.Entries...)}
}

func (s *Store) persistAudit(audit controlAudit) (bool, error) {
	dir := filepath.Dir(s.auditPath())
	tmp, err := os.CreateTemp(dir, ".observability-audit-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := json.NewEncoder(tmp).Encode(audit); err != nil {
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
	if err := os.Rename(tmp.Name(), s.auditPath()); err != nil {
		return false, err
	}
	//nolint:gosec // The parent directory is the validated local config volume.
	handle, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer func() { _ = handle.Close() }()
	return true, handle.Sync()
}
