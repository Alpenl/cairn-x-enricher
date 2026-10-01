package dashboard

import (
	"net/http"
	"strings"
)

func (s *Server) withOfflineScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if backend, ok := s.backend.(interface{ OfflineScope() string }); ok && strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("X-Cairn-Offline-Scope", backend.OfflineScope())
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) offlineScope(writer http.ResponseWriter, _ *http.Request) {
	backend, ok := s.backend.(interface{ OfflineScope() string })
	if !ok {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "offline_unsupported"})
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]string{"scope": backend.OfflineScope()})
}
