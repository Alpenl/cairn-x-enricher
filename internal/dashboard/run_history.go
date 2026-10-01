package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type runHistoryBackend interface {
	GetRunSummaryPage(context.Context, int64, int64) (cairn.RunSummaryPage, error)
	GetRunDetail(context.Context, int64, int64) (cairn.StoredRun, error)
}

func (s *Server) runHistory(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	backend, ok := s.backend.(runHistoryBackend)
	if !ok {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false})
		return
	}
	if raw := request.PathValue("run_id"); raw != "" {
		runID, err := positiveID(raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_id")
			return
		}
		run, err := backend.GetRunDetail(request.Context(), id, runID)
		if err != nil {
			s.writeBackendError(writer, "get run detail", id, err)
			return
		}
		// Keep raw provider metadata and source evidence out of this compact
		// diagnostic view. The administrative replay path can load them itself.
		writeJSON(writer, http.StatusOK, map[string]any{
			"id": run.ID, "status": run.Status, "coverage": run.Coverage,
			"policy_version": run.PolicyVersion, "model": run.ResolvedModel,
			"created_at": run.CreatedAt, "archived": run.Archived,
			"answers": run.Answers, "usage": run.Usage,
		})
		return
	}
	var cursor int64
	if raw := request.URL.Query().Get("after_id"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 1 {
			writeError(writer, http.StatusBadRequest, "invalid_query")
			return
		}
	}
	page, err := backend.GetRunSummaryPage(request.Context(), id, cursor)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get run summaries", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}
