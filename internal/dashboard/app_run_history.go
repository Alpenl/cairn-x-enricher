package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type appRunHistoryReader interface {
	GetAppRunHistory(context.Context, int64, int64, int64) (json.RawMessage, error)
}

// Unlike the compact Web diagnostic route, this opt-in contract includes the
// selected run's stored judgments/model/usage and is suitable for user export.
func (s *Server) appRunHistory(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if request.Header.Get("X-Cairn-App-Run-History") != "1" {
		writeError(writer, http.StatusConflict, "capability_mismatch")
		return
	}
	parse := func(raw string) (int64, bool) {
		value, err := strconv.ParseInt(raw, 10, 64)
		return value, err == nil && value > 0 && value <= 1<<53-1 && strconv.FormatInt(value, 10) == raw
	}
	id, ok := parse(request.PathValue("id"))
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	var runID, after int64
	if raw := request.PathValue("run_id"); raw != "" {
		if runID, ok = parse(raw); !ok {
			writeError(writer, http.StatusBadRequest, "invalid_id")
			return
		}
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) > 1 || len(query) > 0 && (runID > 0 || len(query["after_id"]) != 1) {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	if len(query) > 0 {
		if after, ok = parse(query.Get("after_id")); !ok {
			writeError(writer, http.StatusBadRequest, "invalid_query")
			return
		}
	}
	reader, ok := s.backend.(appRunHistoryReader)
	if !ok {
		writeError(writer, http.StatusNotImplemented, "run_history_unsupported")
		return
	}
	payload, err := reader.GetAppRunHistory(request.Context(), id, runID, after)
	switch {
	case errors.Is(err, cairn.ErrV2Unsupported):
		writeError(writer, http.StatusNotImplemented, "run_history_unsupported")
	case errors.Is(err, cairn.ErrRunHistoryUnavailable):
		writer.Header().Set("X-Cairn-App-Run-History", "1")
		writeError(writer, http.StatusNotFound, "run_unavailable")
	case err != nil:
		s.writeBackendError(writer, "read stored run history", id, err)
	default:
		writer.Header().Set("X-Cairn-App-Run-History", "1")
		writeJSON(writer, http.StatusOK, payload)
	}
}
