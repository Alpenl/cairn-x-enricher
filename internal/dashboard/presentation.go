package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
)

type presentationBackend interface {
	Presentation(context.Context, int64, string, bool) (json.RawMessage, error)
}

func (s *Server) presentation(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost && !s.formattingEnabled {
		writeError(writer, 409, "formatting_unavailable")
		return
	}
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, 400, "invalid_id")
		return
	}
	backend, ok := s.backend.(presentationBackend)
	if !ok {
		writeError(writer, 409, "presentation_unsupported")
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	if request.Method == http.MethodPost && !decodeActionBody(writer, request, &body) {
		return
	}
	payload, err := backend.Presentation(request.Context(), id, request.Method, body.Force)
	if err != nil {
		s.writeBackendError(writer, "presentation", id, err)
		return
	}
	writeJSON(writer, 200, payload)
}
