package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var collectionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type collectionsBackend interface {
	Collections(context.Context, string, string, any) (json.RawMessage, error)
}

func (s *Server) collections(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.backend.(collectionsBackend)
	if !ok {
		writeError(w, 409, "collections_unsupported")
		return
	}
	if id := r.PathValue("collection"); id != "" && !collectionIDPattern.MatchString(id) {
		writeError(w, 400, "invalid_collection")
		return
	}
	var body any
	if r.Method == http.MethodPost {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, 403, "cross_origin")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				writeError(w, 403, "cross_origin")
				return
			}
		}
		var value map[string]any
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if !decodeActionBody(w, r, &value) {
			return
		}
		body = value
	}
	path := strings.Replace(r.URL.Path, "/api/collections", "/api/enrichment/collections", 1)
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.Query().Encode()
	}
	payload, err := backend.Collections(r.Context(), r.Method, path, body)
	if err != nil {
		s.writeBackendError(w, "collections", 0, err)
		return
	}
	if r.Method != http.MethodGet {
		s.invalidateOverview()
	}
	w.Header().Set("X-Cairn-Collections", "1")
	writeJSON(w, http.StatusOK, payload)
}
