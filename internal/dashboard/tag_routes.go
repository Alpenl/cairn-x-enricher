package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// TagSystemBackend is negotiated independently from v2 so old Workers keep
// their original editor and never receive an unrepresentable tag mutation.
type TagSystemBackend interface {
	GetV2TagSystem(context.Context, string) (json.RawMessage, error)
	MutateV2TagSystem(context.Context, string, string, any) (json.RawMessage, error)
}

func (s *Server) tagSystemProxy(writer http.ResponseWriter, request *http.Request) {
	backend, ok := s.backend.(TagSystemBackend)
	if !ok {
		if request.Method == http.MethodGet {
			writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "tag_system_unsupported"})
		} else {
			writeError(writer, http.StatusConflict, "tag_system_unsupported")
		}
		return
	}
	var path string
	var id int64
	switch {
	case request.PathValue("id") != "" && strings.HasPrefix(request.URL.Path, "/api/bookmarks/"):
		var err error
		id, err = positiveID(request.PathValue("id"))
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_id")
			return
		}
		suffix := "tags"
		if strings.HasSuffix(request.URL.Path, "/tag-history") {
			suffix = "tag-history"
		}
		path = "/api/v2/links/" + request.PathValue("id") + "/" + suffix
	case strings.HasPrefix(request.URL.Path, "/api/custom-tags"):
		path = "/api/v2/custom-tags"
		if customID := request.PathValue("id"); customID != "" {
			if len(customID) > 80 || strings.ContainsAny(customID, "/\\ ?#") {
				writeError(writer, http.StatusBadRequest, "invalid_id")
				return
			}
			path += "/" + url.PathEscape(customID)
		}
	case request.URL.Path == "/api/tag-counts":
		path = "/api/v2/tags/counts"
	case request.URL.Path == "/api/tag-quality":
		path = "/api/v2/tags/quality"
	case request.URL.Path == "/api/tag-export":
		path = "/api/v2/tags/export"
	default:
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if request.URL.RawQuery != "" {
		path += "?" + request.URL.Query().Encode()
	}
	var payload json.RawMessage
	var err error
	if request.Method == http.MethodGet {
		payload, err = backend.GetV2TagSystem(request.Context(), path)
	} else {
		var body map[string]any
		if !decodeActionBody(writer, request, &body) {
			return
		}
		if key, valid := body["operation_key"].(string); !valid || key == "" || len(key) > 200 {
			writeError(writer, http.StatusBadRequest, "invalid_operation_key")
			return
		}
		payload, err = backend.MutateV2TagSystem(request.Context(), request.Method, path, body)
	}
	if errors.Is(err, cairn.ErrV2Unsupported) {
		if request.Method == http.MethodGet {
			writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "tag_system_unsupported"})
		} else {
			writeError(writer, http.StatusConflict, "tag_system_unsupported")
		}
		return
	}
	if err != nil {
		s.writeBackendError(writer, "tag system", id, err)
		return
	}
	if request.Method != http.MethodGet {
		s.invalidateOverview()
		if strings.HasPrefix(request.URL.Path, "/api/custom-tags") {
			s.catalog.Invalidate()
		}
	}
	if request.URL.Query().Get("topic_refinements") != "" {
		writer.Header().Set("X-Cairn-Topic-Granularity", "1")
		writer.Header().Set("X-Cairn-Tag-System", "1")
	}
	writeJSON(writer, http.StatusOK, payload)
}

type exportTags struct {
	Selection struct {
		Topics           []string `json:"topics"`
		ResourceKinds    []string `json:"resource_kinds"`
		ContentFunctions []string `json:"content_functions"`
	} `json:"selection"`
	CustomTags []struct {
		Label string `json:"label"`
	} `json:"custom_tags"`
	State struct {
		Fields map[string]struct {
			Values []struct {
				Term      string `json:"term"`
				Origin    string `json:"origin"`
				Confirmed bool   `json:"confirmed"`
			} `json:"values"`
		} `json:"fields"`
	} `json:"state"`
}

func (tags *exportTags) CustomLabels() []string {
	labels := make([]string, 0, len(tags.CustomTags))
	for _, tag := range tags.CustomTags {
		labels = append(labels, tag.Label)
	}
	return labels
}
func (s *Server) hydrateExportTags(ctx context.Context, items []cairn.Bookmark) ([]*exportTags, error) {
	results := make([]*exportTags, len(items))
	backend, ok := s.backend.(TagSystemBackend)
	if !ok || len(items) == 0 {
		return results, nil
	}
	var wait sync.WaitGroup
	var once sync.Once
	var firstErr error
	tasks := make(chan int)
	for range min(exportHydrators, len(items)) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range tasks {
				payload, err := backend.GetV2TagSystem(ctx, "/api/v2/links/"+strconv.FormatInt(items[index].ID, 10)+"/tags")
				if errors.Is(err, cairn.ErrV2Unsupported) {
					continue
				}
				if err != nil {
					once.Do(func() { firstErr = err })
					continue
				}
				var result exportTags
				if err = json.Unmarshal(payload, &result); err != nil {
					once.Do(func() { firstErr = err })
					continue
				}
				results[index] = &result
			}
		}()
	}
	for index := range items {
		tasks <- index
	}
	close(tasks)
	wait.Wait()
	return results, firstErr
}
