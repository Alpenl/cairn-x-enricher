package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type mediaBackend interface {
	Media(context.Context, int64) (json.RawMessage, error)
	MediaFile(context.Context, string, string) (*http.Response, error)
}

func (s *Server) mediaList(w http.ResponseWriter, r *http.Request) {
	id, err := positiveID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid_id")
		return
	}
	backend, ok := s.backend.(mediaBackend)
	if !ok {
		writeJSON(w, 200, map[string]any{"items": []any{}})
		return
	}
	payload, err := backend.Media(r.Context(), id)
	if err != nil {
		s.writeBackendError(w, "media", id, err)
		return
	}
	writeJSON(w, 200, payload)
}

func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	w.Header().Set("Cache-Control", "private, no-store")
	backend, ok := s.backend.(mediaBackend)
	if !ok {
		writeError(w, 404, "not_found")
		return
	}
	response, err := backend.MediaFile(r.Context(), r.PathValue("id"), r.Header.Get("Range"))
	if err != nil {
		s.writeBackendError(w, "media file", 0, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 && response.StatusCode != 206 {
		writeError(w, response.StatusCode, "media_unavailable")
		return
	}
	mime := response.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "video/") && !strings.HasPrefix(mime, "audio/") && mime != "application/zip" {
		writeError(w, 502, "invalid_media_type")
		return
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Content-Disposition"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}
