package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// preferencesBackend forwards cross-device AI tagging preferences and the
// read-only legacy "drop" count shown on the settings page.
type preferencesBackend interface {
	Preferences(context.Context, string, string, any) (json.RawMessage, error)
}

func (s *Server) preferences(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.backend.(preferencesBackend)
	if !ok {
		writeError(w, http.StatusConflict, "preferences_unsupported")
		return
	}
	var body any
	if r.Method == http.MethodPost {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross_origin")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				writeError(w, http.StatusForbidden, "cross_origin")
				return
			}
		}
		var value map[string]any
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		if !decodeActionBody(w, r, &value) {
			return
		}
		if key, valid := value["operation_key"].(string); !valid || key == "" || len(key) > 200 {
			writeError(w, http.StatusBadRequest, "invalid_operation_key")
			return
		}
		body = value
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	var path string
	switch r.URL.Path {
	case "/api/preferences":
		path = "/api/enrichment/preferences"
	case "/api/preferences/operations":
		path = "/api/enrichment/preferences/operations"
	case "/api/links/legacy-dropped":
		path = "/api/enrichment/links/legacy-dropped"
	default:
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	payload, err := backend.Preferences(r.Context(), r.Method, path, body)
	if err != nil {
		s.writeBackendError(w, "preferences", 0, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
