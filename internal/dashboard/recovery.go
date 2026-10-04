package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type sourceRecovery interface {
	SourceRecoveryStatus(context.Context) (cairn.ProviderCheckStatus, error)
	RecoverSource(context.Context) (cairn.ProviderCheckStatus, error)
}

func (s *Server) recoverService(w http.ResponseWriter, r *http.Request) {
	if s.requestCtx.Err() != nil {
		writeError(w, 503, "shutting_down")
		return
	}
	// Same-origin JSON mutation; the public app token never reaches the Worker route.
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		writeError(w, 400, "invalid_content_type")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			writeError(w, 403, "cross_origin")
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, 403, "cross_origin")
		return
	}
	var body map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := decoder.Decode(&body); err != nil || body == nil || len(body) != 0 {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_request")
		return
	}
	recovery, ok := s.processor.(sourceRecovery)
	if !ok {
		writeError(w, 503, "recovery_unavailable")
		return
	}
	status, err := recovery.RecoverSource(r.Context())
	if err != nil {
		s.writeBackendError(w, "request source recovery", 0, err)
		return
	}
	status.LeaseToken = ""
	if status.Accepted || status.State == "pending" {
		if s.wakeup != nil {
			select {
			case s.wakeup <- struct{}{}:
			default:
			}
		}
	}
	writeJSON(w, http.StatusAccepted, status)
}
