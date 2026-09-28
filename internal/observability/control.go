// Package observability holds the local, persistent diagnostic control plane.
package observability

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const maxControlBody = 2048

type logUpdate struct {
	ExpectedVersion uint64  `json:"expected_version"`
	Mode            LogMode `json:"mode"`
	DurationSeconds int64   `json:"duration_seconds"`
}

// Handler is served on a dedicated container-loopback listener. It also checks
// the peer and Origin: same-origin checks alone do not authenticate LAN users.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/observability", func(w http.ResponseWriter, r *http.Request) {
		if !allowLocal(w, r) {
			return
		}
		writeControlJSON(w, http.StatusOK, s.Snapshot())
	})
	mux.HandleFunc("GET /v1/observability/audit", func(w http.ResponseWriter, r *http.Request) {
		if !allowLocal(w, r) {
			return
		}
		if err := s.PruneAudit(); err != nil {
			writeControlJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_unavailable"})
			return
		}
		writeControlJSON(w, http.StatusOK, s.Audit())
	})
	mux.HandleFunc("PUT /v1/observability/logs", func(w http.ResponseWriter, r *http.Request) {
		if !allowLocal(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input logUpdate
		if err := decoder.Decode(&input); err != nil {
			writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_config"})
			return
		}
		var rest any
		if err := decoder.Decode(&rest); !errors.Is(err, io.EOF) {
			writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_config"})
			return
		}
		if input.DurationSeconds < 0 || input.DurationSeconds > int64(MaxDiagnostic/time.Second) {
			writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_config"})
			return
		}
		status, err := s.Update(input.ExpectedVersion, input.Mode, time.Duration(input.DurationSeconds)*time.Second)
		switch {
		case errors.Is(err, ErrVersionConflict):
			writeControlJSON(w, http.StatusConflict, map[string]string{"error": "version_conflict"})
		case errors.Is(err, ErrRateLimited):
			w.Header().Set("Retry-After", "1")
			writeControlJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		case errors.Is(err, ErrInvalidPolicy):
			writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_config"})
		case errors.Is(err, ErrAuditUnavailable):
			writeControlJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "audit_unavailable", "applied_version": status.AppliedVersion})
		case err != nil:
			writeControlJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "persistence_unconfirmed", "applied_version": status.AppliedVersion})
		default:
			writeControlJSON(w, http.StatusOK, status)
		}
	})
	return mux
}

func allowLocal(w http.ResponseWriter, r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
		writeControlJSON(w, http.StatusForbidden, map[string]string{"error": "local_only"})
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		writeControlJSON(w, http.StatusForbidden, map[string]string{"error": "local_only"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || parsed.Host != r.Host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			writeControlJSON(w, http.StatusForbidden, map[string]string{"error": "origin_rejected"})
			return false
		}
	}
	return true
}

func writeControlJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
