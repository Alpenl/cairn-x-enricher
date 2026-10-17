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

type processingBackend interface {
	GetProcessing(context.Context, int64) (json.RawMessage, error)
	SubmitProcessing(context.Context, int64, cairn.ProcessingIntent) (json.RawMessage, error)
}

func (s *Server) processing(writer http.ResponseWriter, request *http.Request) {
	const capability = cairn.ProcessingCapability
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
	backend, ok := s.backend.(processingBackend)
	if !ok {
		writeError(writer, 501, "processing_unsupported")
		return
	}
	var payload json.RawMessage
	if request.Method == http.MethodGet {
		payload, err = backend.GetProcessing(request.Context(), id)
	} else {
		body, readErr := io.ReadAll(io.LimitReader(request.Body, (128<<10)+1))
		var fields map[string]json.RawMessage
		var intent cairn.ProcessingIntent
		exact := json.Unmarshal(body, &fields) == nil
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if readErr != nil || len(body) > 128<<10 || !utf8.Valid(body) || !exact || decoder.Decode(&intent) != nil || decoder.Decode(&struct{}{}) != io.EOF || !intent.Valid() {
			writeError(writer, 400, "invalid_action")
			return
		}
		names := []string{"version", "operation_key", "fence", "action", "expected_content_revision", "target_generation"}
		if intent.Action == "save_primary" {
			names = append(names, "text")
		}
		exact = len(fields) == len(names)
		for _, name := range names {
			if value, present := fields[name]; !present || string(value) == "null" {
				exact = false
			}
		}
		if !exact {
			writeError(writer, 400, "invalid_action")
			return
		}
		payload, err = backend.SubmitProcessing(request.Context(), id, intent)
		if err == nil {
			var receipt struct {
				Reading        bool `json:"reading_queued"`
				Classification bool `json:"classification_queued"`
				Replayed       bool `json:"replayed"`
			}
			_ = json.Unmarshal(payload, &receipt)
			if !receipt.Replayed {
				if receipt.Reading && s.wakeup != nil {
					select {
					case s.wakeup <- struct{}{}:
					default:
					}
				}
				if receipt.Classification && s.classificationWakeup != nil {
					select {
					case s.classificationWakeup <- struct{}{}:
					default:
					}
				}
			}
		}
	}
	if errors.Is(err, cairn.ErrInvalidProcessing) {
		writer.Header().Set(capability, "1")
		writeError(writer, 400, "invalid_action")
	} else if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, 501, "processing_unsupported")
	} else if err != nil {
		var api *cairn.APIError
		if errors.As(err, &api) && (api.StatusCode == 400 || api.StatusCode == 404 || api.StatusCode == 409 || api.StatusCode == 413) {
			writer.Header().Set(capability, "1")
			writeError(writer, api.StatusCode, api.Code)
		} else {
			s.writeBackendError(writer, "processing", id, err)
		}
	} else {
		writer.Header().Set(capability, "1")
		writeJSON(writer, 200, payload)
	}
}
