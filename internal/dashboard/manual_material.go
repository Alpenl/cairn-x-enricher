package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type manualMaterialBackend interface {
	GetManualMaterial(context.Context, int64) (json.RawMessage, error)
	SubmitManualMaterial(context.Context, int64, cairn.ManualMaterialIntent) (json.RawMessage, error)
}

func (s *Server) manualMaterial(writer http.ResponseWriter, request *http.Request) {
	const capability = "X-Cairn-App-Manual-Material"
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writeError(writer, 405, "method_not_allowed")
		return
	}
	if request.Header.Get(capability) != "1" {
		writeError(writer, 409, "capability_mismatch")
		return
	}
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > 1<<53-1 || strconv.FormatInt(id, 10) != request.PathValue("id") || request.URL.RawQuery != "" {
		writeError(writer, 400, "invalid_request")
		return
	}
	backend, ok := s.backend.(manualMaterialBackend)
	if !ok {
		writeError(writer, 501, "manual_material_unsupported")
		return
	}
	var payload json.RawMessage
	if request.Method == http.MethodGet {
		payload, err = backend.GetManualMaterial(request.Context(), id)
	} else {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, (128<<10)+1))
		var intent cairn.ManualMaterialIntent
		var fields map[string]json.RawMessage
		exactFields := json.Unmarshal(body, &fields) == nil && len(fields) == 9
		for _, name := range []string{"version", "operation_key", "expected_content_revision", "evidence_snapshot_id", "source_hash", "target_generation", "kind", "source_ref", "text"} {
			if _, present := fields[name]; !present {
				exactFields = false
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if readErr != nil || len(body) > 128<<10 || !utf8.Valid(body) || !exactFields || decoder.Decode(&intent) != nil || decoder.Decode(&struct{}{}) != io.EOF || !intent.Valid() {
			writeError(writer, 400, "invalid_material")
			return
		}
		payload, err = backend.SubmitManualMaterial(request.Context(), id, intent)
		if err == nil {
			// A replay is an acknowledgement, never another wakeup or new intent.
			var receipt struct {
				Queued   bool `json:"classification_queued"`
				Replayed bool `json:"replayed"`
			}
			_ = json.Unmarshal(payload, &receipt)
			if receipt.Queued && !receipt.Replayed && s.classificationWakeup != nil {
				select {
				case s.classificationWakeup <- struct{}{}:
				default:
				}
			}
		}
	}
	if errors.Is(err, cairn.ErrInvalidManualMaterial) {
		writer.Header().Set(capability, "1")
		writeError(writer, 400, "invalid_material")
	} else if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, 501, "manual_material_unsupported")
	} else if err != nil {
		var api *cairn.APIError
		if errors.As(err, &api) && (api.StatusCode == 400 || api.StatusCode == 404 || api.StatusCode == 409 || api.StatusCode == 413 || api.StatusCode == 503) {
			writer.Header().Set(capability, "1")
			writeError(writer, api.StatusCode, api.Code)
		} else {
			s.writeBackendError(writer, "manual material", id, err)
		}
	} else {
		writer.Header().Set(capability, "1")
		writeJSON(writer, 200, payload)
	}
}
