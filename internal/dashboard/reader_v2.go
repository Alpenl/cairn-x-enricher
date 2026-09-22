package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	payload, err := v2.RefreshSource(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusConflict, "v2_unsupported")
		return
	}
	if err != nil {
		s.writeBackendError(writer, "refresh source", id, err)
		return
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

// exportMarkdown streams a bounded Markdown export that carries every effective
// v2 dimension, why/source, human origin and partial counts. It makes no model
// call and changes no curation state (B05-T13/B06-T09).
func (s *Server) exportMarkdown(writer http.ResponseWriter, request *http.Request) {
	// The export honours the same filters as the list so the file matches what
	// the user is looking at; the limit is bounded because the export hydrates
	// each bookmark's effective view.
	query, err := bookmarkQuery(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	if query.Limit < 1 || query.Limit > 500 {
		query.Limit = 200
	}
	page, err := s.backend.ListBookmarks(request.Context(), query)
	if err != nil {
		s.writeBackendError(writer, "export bookmarks", 0, err)
		return
	}
	v2, hasV2 := s.backend.(V2Backend)
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Cairn 收藏导出\n\n生成时间：%s\n\n", exportTimestamp())
	fmt.Fprintf(&builder, "本页 %d 条；导出包含多维有效结果与人工来源，不调用模型。\n\n", len(page.Items))
	partial := 0
	for _, item := range page.Items {
		fmt.Fprintf(&builder, "## %s\n\n", exportLine(item.URL))
		fmt.Fprintf(&builder, "- 收藏 ID：%d\n- 来源：%s\n- 整理状态：%s\n", item.ID, exportLine(item.Source), exportLine(item.CurationStatus))
		if hasV2 {
			if payload, err := v2.GetV2Effective(request.Context(), item.ID); err == nil {
				var view struct {
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
				if json.Unmarshal(payload, &view) == nil {
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
				}
			}
		}
		for label, value := range map[string]string{"收藏原因": item.Why, "收藏备注": item.Note, "AI 标题": item.AITitle, "摘要": item.Summary} {
			if strings.TrimSpace(value) != "" {
				fmt.Fprintf(&builder, "\n### %s\n\n%s\n", label, exportLine(value))
			}
		}
		builder.WriteString("\n")
	}
	fmt.Fprintf(&builder, "---\n\n部分/过期结果：%d 条（共 %d 条）。\n", partial, len(page.Items))
	writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="cairn-export.md"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, builder.String())
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
		writeJSON(writer, http.StatusOK, map[string]any{"applied": false, "reason": "rerank disabled"})
		return
	}
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decodeActionBody(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Query) == "" || len(body.Query) > 200 {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	if body.Limit < 1 || body.Limit > 20 {
		body.Limit = 10
	}
	page, err := s.backend.ListBookmarks(request.Context(), cairn.BookmarkQuery{
		Limit: body.Limit, Search: body.Query, SummaryOnly: true,
	})
	if err != nil {
		s.writeBackendError(writer, "rerank candidates", 0, err)
		return
	}
	candidates := make([]extension.Candidate, 0, len(page.Items))
	for index, item := range page.Items {
		text := strings.TrimSpace(item.AITitle + " " + item.Summary)
		if len([]rune(text)) > 400 {
			text = string([]rune(text)[:400])
		}
		candidates = append(candidates, extension.Candidate{
			ID: strconv.FormatInt(item.ID, 10), Text: text, Rank: index, Allowed: true,
		})
	}
	result := s.extensions.RerankCandidates(request.Context(), body.Query, candidates)
	writeJSON(writer, http.StatusOK, result)
}
