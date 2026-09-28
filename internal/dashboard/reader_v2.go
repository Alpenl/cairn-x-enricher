package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

// getEvidence returns the stored immutable evidence snapshot. The UI renders
// only these blocks; a missing snapshot is reported as unavailable instead of
// being replaced by a generated explanation (B06-T05).
func (s *Server) getEvidence(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	payload, err := v2.GetEvidence(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get evidence", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, payload)
}

// getClassificationStatus reports the queue state so pending/processing/failed/
// exhausted is never confused with a legitimate empty classification.
func (s *Server) getClassificationStatus(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	payload, err := v2.GetClassificationStatus(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get classification status", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, payload)
}

// getEntities reports the independent entity lifecycle plus human corrections.
func (s *Server) getEntities(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	payload, err := v2.GetEntities(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get entities", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, payload)
}

// correctEntity records a human entity correction through the same override log
// the field UI uses, so the human value always wins over a later run.
func (s *Server) correctEntity(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	var body map[string]any
	if !decodeActionBody(writer, request, &body) {
		return
	}
	if body["operation_key"] == nil || body["action"] == nil {
		writeError(writer, http.StatusBadRequest, "invalid_override")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	if err := v2.CorrectEntity(request.Context(), id, body); err != nil {
		if errors.Is(err, cairn.ErrV2Unsupported) {
			writeError(writer, http.StatusConflict, "v2_unsupported")
			return
		}
		s.writeBackendError(writer, "correct entity", id, err)
		return
	}
	payload, err := v2.GetEntities(request.Context(), id)
	if err != nil {
		s.writeBackendError(writer, "get entities after correction", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, payload)
}

// retryClassification re-arms only the classification queue. It never
// re-fetches the source and never calls a model by itself (B06-T07).
func (s *Server) retryClassification(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	if err := v2.RetryClassification(request.Context(), id); err != nil {
		if errors.Is(err, cairn.ErrV2Unsupported) {
			writeError(writer, http.StatusConflict, "v2_unsupported")
			return
		}
		s.writeBackendError(writer, "retry classification", id, err)
		return
	}
	if s.wakeup != nil {
		select {
		case s.wakeup <- struct{}{}:
		default:
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": id, "action": "retry_classification", "model_calls": 0,
		"detail": "只重新入分类队列；不抓取来源，不立即调用模型。",
	})
}

// refreshSource schedules a real bounded retrieval. It is explicitly different
// from a classification retry: it costs a retrieval, keeps the old readable
// content and all human curation until new bytes arrive (B06-T07/F13).
func (s *Server) refreshSource(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		OperationKey string `json:"operation_key"`
	}
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if body.OperationKey == "" {
		body.OperationKey, err = newManualOperationKey()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "backend_error")
			return
		}
	}
	if len(body.OperationKey) > 200 {
		writeError(writer, http.StatusBadRequest, "invalid_operation_key")
		return
	}
	var payload json.RawMessage
	if operational, supported := v2.(interface {
		RefreshSourceWithOperation(context.Context, int64, string) (json.RawMessage, error)
	}); supported {
		payload, err = operational.RefreshSourceWithOperation(request.Context(), id, body.OperationKey)
	} else {
		payload, err = v2.RefreshSource(request.Context(), id)
	}
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusConflict, "v2_unsupported")
		return
	}
	if err != nil {
		var apiErr *cairn.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			writer.Header().Set("Retry-After", apiErr.RetryAfter)
			writeError(writer, http.StatusTooManyRequests, "manual_queue_full")
			return
		}
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict &&
			(apiErr.Code == "lease_conflict" || apiErr.Code == "input_changed" ||
				apiErr.Code == "operation_conflict") {
			writeError(writer, http.StatusConflict, apiErr.Code)
			return
		}
		s.writeBackendError(writer, "refresh source", id, err)
		return
	}
	if s.wakeup != nil {
		select {
		case s.wakeup <- struct{}{}:
		default:
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": id, "action": "refresh_source", "fetch": true, "worker": json.RawMessage(payload),
		"detail": "重新抓取原文；旧内容与人工整理在新内容到达前保持不变。",
	})
}

// replayPolicy re-decides the stored run under a policy without any model call.
// Dry-run is the default; a commit appends a decision and requires the explicit
// CAIRN_ALLOW_DECISION_WRITE opt-in.
func (s *Server) replayPolicy(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	var body struct {
		TopicAccept  *float64 `json:"topic_accept"`
		TopicReject  *float64 `json:"topic_reject"`
		ChoiceAccept *float64 `json:"choice_accept"`
		Commit       bool     `json:"commit"`
	}
	if !decodeActionBody(writer, request, &body) {
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	runs, err := v2.GetRuns(request.Context(), id)
	if err != nil {
		s.writeBackendError(writer, "get runs", id, err)
		return
	}
	run, err := newestReplayableRun(runs)
	if err != nil {
		writeError(writer, http.StatusConflict, "no_replayable_run")
		return
	}
	storedSpec, err := v2.GetQuestionSpec(request.Context(), run.SpecID)
	if err != nil {
		s.writeBackendError(writer, "get question spec", id, err)
		return
	}
	spec, err := classify.DecodeSpec(storedSpec.Payload)
	if err != nil {
		writeError(writer, http.StatusConflict, "spec_not_decodable")
		return
	}
	raw, err := run.DecodeJudgments(spec)
	if err != nil {
		writeError(writer, http.StatusConflict, "run_not_replayable")
		return
	}
	historical, err := classify.DecodePolicy(run.Policy)
	if err != nil {
		writeError(writer, http.StatusConflict, "historical_policy_missing")
		return
	}
	next := historical
	next.Version = historical.Version + "+replay"
	if body.TopicAccept != nil {
		next.TopicAccept = *body.TopicAccept
	}
	if body.TopicReject != nil {
		next.TopicReject = *body.TopicReject
	}
	if body.ChoiceAccept != nil {
		next.ChoiceAccept = *body.ChoiceAccept
	}
	before, after, changed, err := classify.Replay(raw, historical, next)
	if err != nil {
		writeError(writer, http.StatusConflict, "replay_failed")
		return
	}
	output := map[string]any{
		"id": id, "run_id": run.ID, "spec_id": run.SpecID,
		"historical_policy": historical.Version, "new_policy": next.Version,
		"model_calls": 0, "changed": changed, "before": before, "after": after,
		"committed": false,
	}
	if body.Commit {
		if os.Getenv("CAIRN_ALLOW_DECISION_WRITE") != "1" {
			output["committed_reason"] = "写回未授权：需要服务端显式设置 CAIRN_ALLOW_DECISION_WRITE=1"
		} else {
			if err := v2.SubmitDecision(request.Context(), id, map[string]any{
				"operation_key":    fmt.Sprintf("ui-replay-%d-%d-%s", id, run.ID, next.Version),
				"run_ids":          []int64{run.ID},
				"policy_version":   next.Version,
				"policy":           next,
				"spec_id":          run.SpecID,
				"spec_hash":        run.SpecHash,
				"resolved_model":   run.ResolvedModel,
				"requested_model":  run.RequestedModel,
				"content_revision": run.ContentRevision,
				"automatic":        classify.AutomaticFromProposals(after),
			}); err != nil {
				s.writeBackendError(writer, "commit replay decision", id, err)
				return
			}
			output["committed"] = true
		}
	}
	writeJSON(writer, http.StatusOK, output)
}

func newestReplayableRun(runs []cairn.StoredRun) (cairn.StoredRun, error) {
	for index := len(runs) - 1; index >= 0; index-- {
		run := runs[index]
		if run.Status == "succeeded" && run.Coverage == "complete" && len(run.Answers) > 0 {
			return run, nil
		}
	}
	return cairn.StoredRun{}, errors.New("no complete succeeded run")
}

// Export bounds. The list endpoint caps one page at maxPageSize for the UI;
// the export pages on its own, up to the Worker's list maximum per request,
// and hydrates each bookmark's effective view with a few concurrent reads.
const (
	defaultExportLimit = 200
	maxExportLimit     = 500
	exportPageSize     = 100
	exportHydrators    = 4
	exportBatchSize    = 50
)

// exportMarkdown streams a bounded Markdown export that carries every effective
// v2 dimension, why/source, human origin and partial counts. It makes no model
// call and changes no curation state (B05-T13/B06-T09).
func (s *Server) exportMarkdown(writer http.ResponseWriter, request *http.Request) {
	// The export honours the same filters as the list so the file matches what
	// the user is looking at. Its own limit is validated here, because a list
	// page is capped far below what an export may cover.
	limit := defaultExportLimit
	values := request.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxExportLimit {
			writeError(writer, http.StatusBadRequest, "invalid_query")
			return
		}
		limit = parsed
	}
	values.Del("limit")
	filtered := request.Clone(request.Context())
	filtered.URL.RawQuery = values.Encode()
	query, err := bookmarkQuery(filtered)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	// Only summary fields are exported, so the large bodies are never read.
	query.SummaryOnly = true
	query.SkipCounts = true
	items, truncated, err := s.collectExport(request.Context(), query, limit)
	if err != nil {
		s.writeBackendError(writer, "export bookmarks", 0, err)
		return
	}
	views, err := s.hydrateExport(request.Context(), items)
	if err != nil {
		s.writeBackendError(writer, "export effective views", 0, err)
		return
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Cairn 收藏导出\n\n生成时间：%s\n\n", exportTimestamp())
	fmt.Fprintf(&builder, "共 %d 条；导出包含多维有效结果与人工来源，不调用模型。\n\n", len(items))
	if truncated {
		fmt.Fprintf(&builder, "已达到单次导出上限 %d 条，还有更多符合条件的收藏没有导出，可缩小筛选范围后分批导出。\n\n", limit)
	}
	partial := 0
	for index, item := range items {
		fmt.Fprintf(&builder, "## %s\n\n", exportLine(item.URL))
		fmt.Fprintf(&builder, "- 收藏 ID：%d\n- 来源：%s\n- 整理状态：%s\n", item.ID, exportLine(item.Source), exportLine(item.CurationStatus))
		if view := views[index]; view != nil {
			fmt.Fprintf(&builder, "- 主题：%s\n", exportList(view.Effective.Topics))
			fmt.Fprintf(&builder, "- 实体：%s\n", exportList(view.Effective.Entities))
			fmt.Fprintf(&builder, "- 内容功能：%s\n", exportList(view.Effective.ContentFunctions))
			fmt.Fprintf(&builder, "- 载体：%s\n", exportList(view.Effective.Carriers))
			fmt.Fprintf(&builder, "- 潜在用途：%s\n", exportList(view.Effective.Affordances))
			fmt.Fprintf(&builder, "- v1 形态/用途：%s / %s\n", exportLine(view.Effective.Form), exportLine(view.Effective.Use))
			fmt.Fprintf(&builder, "- 结果来源：%s；人工整理：%t；过期：%t\n", map[bool]string{true: "decision", false: "legacy"}[view.Projected], view.Effective.Reviewed, view.Stale)
			if !view.Projected || view.Stale {
				partial++
			}
		} else {
			partial++
			builder.WriteString("- 有效结果：不可用（当前 Worker 未提供 v2 有效视图）\n")
		}
		for _, field := range []struct{ label, value string }{
			{"收藏原因", item.Why}, {"收藏备注", item.Note}, {"AI 标题", item.AITitle}, {"摘要", item.Summary},
		} {
			if strings.TrimSpace(field.value) != "" {
				fmt.Fprintf(&builder, "\n### %s\n\n%s\n", field.label, exportLine(field.value))
			}
		}
		builder.WriteString("\n")
	}
	fmt.Fprintf(&builder, "---\n\n部分/过期结果：%d 条（共 %d 条）。\n", partial, len(items))
	writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="cairn-export.md"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, builder.String())
}

// collectExport pages through the filtered list until the limit is reached.
// truncated reports that more matching bookmarks exist beyond the limit.
func (s *Server) collectExport(ctx context.Context, query cairn.BookmarkQuery, limit int) ([]cairn.Bookmark, bool, error) {
	items := make([]cairn.Bookmark, 0, min(limit, exportPageSize))
	for {
		query.Limit = min(exportPageSize, limit-len(items))
		page, err := s.backend.ListBookmarks(ctx, query)
		if err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if page.NextBeforeID == nil || len(page.Items) == 0 {
			return items, false, nil
		}
		if len(items) >= limit {
			return items[:limit], true, nil
		}
		query.BeforeID = *page.NextBeforeID
	}
}

type exportView struct {
	Effective struct {
		Topics           []string `json:"topics"`
		ContentFunctions []string `json:"content_functions"`
		Carriers         []string `json:"carriers"`
		Affordances      []string `json:"affordances"`
		Form             string   `json:"form"`
		Use              string   `json:"use"`
		Reviewed         bool     `json:"reviewed"`
		Entities         []string `json:"entities"`
	} `json:"effective"`
	Projected bool `json:"projected"`
	Stale     bool `json:"stale"`
}

type batchEffectiveBackend interface {
	GetV2EffectiveBatch(context.Context, []int64) (map[int64]json.RawMessage, error)
}

// hydrateExport uses one Worker SQL read per 50 views when available. An old
// Worker returns 404/405 for the batch endpoint, so the existing per-link path
// remains usable during the Worker-first rollout.
func (s *Server) hydrateExport(ctx context.Context, items []cairn.Bookmark) ([]*exportView, error) {
	views := make([]*exportView, len(items))
	v2, ok := s.backend.(V2Backend)
	if !ok || len(items) == 0 {
		return views, nil
	}
	if batch, supported := s.backend.(batchEffectiveBackend); supported {
		for start := 0; start < len(items); start += exportBatchSize {
			end := min(start+exportBatchSize, len(items))
			ids := make([]int64, 0, end-start)
			for _, item := range items[start:end] {
				ids = append(ids, item.ID)
			}
			payloads, err := batch.GetV2EffectiveBatch(ctx, ids)
			if errors.Is(err, cairn.ErrV2Unsupported) {
				return s.hydrateExportSingles(ctx, items, v2)
			}
			if err != nil {
				return nil, err
			}
			for index := start; index < end; index++ {
				payload, found := payloads[items[index].ID]
				if !found {
					return nil, fmt.Errorf("export effective view %d disappeared; retry export", items[index].ID)
				}
				var view exportView
				if err := json.Unmarshal(payload, &view); err != nil {
					return nil, fmt.Errorf("decode export effective view: %w", err)
				}
				views[index] = &view
			}
		}
		return views, nil
	}
	return s.hydrateExportSingles(ctx, items, v2)
}

// hydrateExportSingles is the compatibility path for older Workers.
func (s *Server) hydrateExportSingles(ctx context.Context, items []cairn.Bookmark, v2 V2Backend) ([]*exportView, error) {
	views := make([]*exportView, len(items))
	next := make(chan int)
	var wait sync.WaitGroup
	var unsupported atomic.Int64
	var firstErr error
	var errorOnce sync.Once
	for range min(exportHydrators, len(items)) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range next {
				payload, err := v2.GetV2Effective(ctx, items[index].ID)
				if errors.Is(err, cairn.ErrV2Unsupported) {
					unsupported.Add(1)
					continue
				}
				if err != nil {
					errorOnce.Do(func() { firstErr = err })
					continue
				}
				var view exportView
				if err := json.Unmarshal(payload, &view); err != nil {
					errorOnce.Do(func() { firstErr = fmt.Errorf("decode export effective view: %w", err) })
					continue
				}
				views[index] = &view
			}
		}()
	}
	for index := range items {
		next <- index
	}
	close(next)
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if count := unsupported.Load(); count > 0 && count != int64(len(items)) {
		return nil, errors.New("some export effective views disappeared; retry export")
	}
	return views, nil
}

func exportTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func exportLine(value string) string {
	replaced := strings.NewReplacer("\r", " ", "\n", " ", "|", "\\|").Replace(value)
	if replaced == "" {
		return "—"
	}
	return replaced
}

func exportList(values []string) string {
	if len(values) == 0 {
		return "（空）"
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, exportLine(value))
	}
	return strings.Join(lines, " / ")
}

// decodeActionBody reads a bounded JSON object for an action endpoint.
func decodeActionBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType := request.Header.Get("Content-Type")
	if !strings.HasPrefix(mediaType, "application/json") {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

// rerank re-orders a bounded, already-filtered candidate set with one shared
// rubric. The candidate set comes from the ordinary list query, so permissions
// and filters are never widened; a disabled flag, an exhausted budget or a
// provider failure returns the original order with an explicit reason
// (B09-T09/T10).
func (s *Server) rerank(writer http.ResponseWriter, request *http.Request) {
	if s.extensions == nil || !s.extensionFlags.Rerank {
		writeJSON(writer, http.StatusOK, map[string]any{"applied": false, "reason": "rerank disabled", "scope": "current_candidates"})
		return
	}
	var body struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Filters string `json:"filters,omitempty"`
	}
	if !decodeActionBody(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Query) == "" || len(body.Query) > 200 || len(body.Filters) > 4096 {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	if body.Limit < 1 || body.Limit > 20 {
		body.Limit = 10
	}
	values, err := url.ParseQuery(body.Filters)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	allowed := map[string]bool{"before_id": true, "status": true, "curation_status": true, "topic": true, "topics": true, "form": true, "use": true, "source": true, "uncertain": true, "since": true, "content_functions": true, "carriers": true, "affordances": true, "entity_state": true, "filter_contract_version": true}
	for key := range values {
		if !allowed[key] {
			writeError(writer, http.StatusBadRequest, "invalid_query")
			return
		}
	}
	if versions, present := values["filter_contract_version"]; present && (len(versions) != 1 || versions[0] != "1") {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	values.Set("q", body.Query)
	values.Set("limit", strconv.Itoa(body.Limit))
	values.Set("view", "summary")
	values.Set("filter_contract_version", "1")
	filtered := request.Clone(request.Context())
	filtered.URL.RawQuery = values.Encode()
	query, err := bookmarkQuery(filtered)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	query.IncludeCacheIdentity = s.extensions.HasRerankStore()
	query.SkipCounts = true
	page, err := s.backend.ListBookmarks(request.Context(), query)
	if err != nil {
		s.writeBackendError(writer, "rerank candidates", 0, err)
		return
	}
	candidates := rerankCandidates(page.Items)
	encoded, _ := json.Marshal(query)
	scope := fmt.Sprintf("%x", sha256.Sum256(encoded))
	result := s.extensions.RerankCandidatesScoped(request.Context(), body.Query, candidates, scope)
	// Re-read the actual filtered page even on cache hits. A source/curation
	// change, deletion or new leading item invalidates this page's result.
	current, err := s.backend.ListBookmarks(request.Context(), query)
	if err != nil {
		s.writeBackendError(writer, "recheck rerank candidates", 0, err)
		return
	}
	latest := rerankCandidates(current.Items)
	if !reflect.DeepEqual(candidates, latest) {
		result = extension.Rerank(latest, nil, false)
		result.Reason = "candidate set changed; showing current original order"
		result.CacheStatus = "invalidated"
	}
	result.Scope = "current_candidates"
	result.NextBeforeID = current.NextBeforeID
	writeJSON(writer, http.StatusOK, result)
}

func rerankCandidates(items []cairn.Bookmark) []extension.Candidate {
	candidates := make([]extension.Candidate, 0, len(items))
	for index, item := range items {
		text := strings.TrimSpace(item.AITitle + " " + item.Summary)
		if len([]rune(text)) > 400 {
			text = string([]rune(text)[:400])
		}
		candidate := extension.Candidate{ID: strconv.FormatInt(item.ID, 10), Text: text, Rank: index, Allowed: true, Role: "reading_summary"}
		if v := item.CacheIdentity; v != nil && v.SchemaVersion == 1 {
			candidate.CacheItem = &extension.CacheItem{ID: item.ID, ContentRevision: v.ContentRevision, BodyRevision: v.BodyRevision, PersonalRevision: v.PersonalRevision, LatestDecisionID: v.LatestDecisionID, LatestEntityRevision: v.LatestEntityRevision}
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}
