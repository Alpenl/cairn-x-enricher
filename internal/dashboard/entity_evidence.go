package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// A narrow optional capability keeps older backends readable without giving
// clients a generic proxy for internal evidence/spec/model management.
type entityEvidenceReader interface {
	GetEntityEvidence(context.Context, int64, cairn.EntityEvidenceReference) (json.RawMessage, error)
}

func (s *Server) getEntityEvidence(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_evidence_reference")
		return
	}
	query, err := cairn.ParseEntityEvidenceReference(values)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_evidence_reference")
		return
	}
	reader, ok := s.backend.(entityEvidenceReader)
	if !ok {
		writeError(writer, http.StatusNotImplemented, "entity_evidence_unsupported")
		return
	}
	payload, err := reader.GetEntityEvidence(request.Context(), id, query)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusNotImplemented, "entity_evidence_unsupported")
		return
	}
	if err == nil || errors.Is(err, cairn.ErrEntityEvidenceUnavailable) || errors.Is(err, cairn.ErrEntityEvidenceChanged) {
		writer.Header().Set("X-Cairn-Entity-Evidence", "1")
	}
	switch {
	case errors.Is(err, cairn.ErrEntityEvidenceUnavailable):
		writeError(writer, http.StatusNotFound, "evidence_unavailable")
	case errors.Is(err, cairn.ErrEntityEvidenceChanged):
		writeError(writer, http.StatusConflict, "evidence_reference_changed")
	case err != nil:
		s.writeBackendError(writer, "read entity evidence", id, err)
	default:
		writeJSON(writer, http.StatusOK, payload)
	}
}
