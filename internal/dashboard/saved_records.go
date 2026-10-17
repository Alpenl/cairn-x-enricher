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

type savedRecordReader interface {
	GetSavedRecords(context.Context, int64, cairn.SavedRecordQuery) (json.RawMessage, error)
}

func (s *Server) savedRecords(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	const capability = "X-Cairn-App-Saved-Records"
	if request.Method != http.MethodGet {
		writeError(writer, 405, "method_not_allowed")
		return
	}
	if request.Header.Get(capability) != "1" {
		writeError(writer, 409, "capability_mismatch")
		return
	}
	parse := func(raw string) (int64, bool) {
		n, err := strconv.ParseInt(raw, 10, 64)
		return n, err == nil && n > 0 && n <= 1<<53-1 && strconv.FormatInt(n, 10) == raw
	}
	id, valid := parse(request.PathValue("id"))
	query, err := url.ParseQuery(request.URL.RawQuery)
	if !valid || err != nil {
		writeError(writer, 400, "invalid_query")
		return
	}
	q := cairn.SavedRecordQuery{Kind: request.PathValue("kind"), RecordKey: query.Get("record_key"), AfterKey: query.Get("after_key")}
	for key, values := range query {
		if (key != "record_key" && key != "after_key" && key != "source_revision") || len(values) != 1 || values[0] == "" {
			writeError(writer, 400, "invalid_query")
			return
		}
	}
	if raw, present := query["source_revision"]; present {
		if q.SourceRevision, valid = parse(raw[0]); !valid || q.Kind != "entities" {
			writeError(writer, 400, "invalid_query")
			return
		}
	}
	if !q.Valid() {
		writeError(writer, 400, "invalid_query")
		return
	}
	reader, ok := s.backend.(savedRecordReader)
	if !ok {
		writeError(writer, 501, "saved_records_unsupported")
		return
	}
	payload, err := reader.GetSavedRecords(request.Context(), id, q)
	switch {
	case errors.Is(err, cairn.ErrV2Unsupported):
		writeError(writer, 501, "saved_records_unsupported")
	case errors.Is(err, cairn.ErrRunHistoryUnavailable):
		writer.Header().Set(capability, "1")
		writeError(writer, 404, "saved_record_unavailable")
	case err != nil:
		s.writeBackendError(writer, "read saved record", id, err)
	default:
		writer.Header().Set(capability, "1")
		writeJSON(writer, 200, payload)
	}
}
