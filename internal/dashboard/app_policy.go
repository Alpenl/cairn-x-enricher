package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

type appPolicyBackend interface {
	GetReplayableRun(context.Context, int64) (*cairn.StoredRun, error)
	GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error)
	GetV2Selection(context.Context, int64) (cairn.V2SelectionView, error)
	Handshake(context.Context, cairn.Capabilities) (cairn.HandshakeResult, error)
	GetPolicyPreview(context.Context, int64, string, string) (json.RawMessage, error)
	PreparePolicyPreview(context.Context, int64, string, cairn.PolicyReplayRequest) (json.RawMessage, error)
	ApplyPolicyPreview(context.Context, int64, string, string) (json.RawMessage, error)
}

var appPolicyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)
var appPolicyHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// SetAppPolicyToken configures the private Worker-to-executor credential before
// serving requests. The rest of the management HTTP surface is not App auth.
func (s *Server) SetAppPolicyToken(token string) { s.appPolicyToken = strings.TrimSpace(token) }

func (s *Server) appPolicy(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	expectedHash, providedHash := sha256.Sum256([]byte(s.appPolicyToken)), sha256.Sum256([]byte(provided))
	if s.appPolicyToken == "" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") ||
		subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if request.Header.Get(cairn.PolicyPreviewCapability) != "1" {
		writeError(writer, http.StatusConflict, "capability_mismatch")
		return
	}
	writer.Header().Set(cairn.PolicyPreviewCapability, "1")
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > 1<<53-1 || strconv.FormatInt(id, 10) != request.PathValue("id") {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	backend, ok := s.backend.(appPolicyBackend)
	if !ok {
		writeError(writer, http.StatusNotImplemented, "policy_replay_unsupported")
		return
	}
	action := request.PathValue("action")
	if action == "capabilities" && request.Method == http.MethodGet {
		if request.URL.RawQuery != "" {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		target, _, err := appPolicyTarget(request.Context(), backend)
		if err != nil {
			s.writeAppPolicyError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"version": 1, "id": id, "supported": target.Supported,
			"can_preview": target.Supported, "can_apply": target.Supported && os.Getenv("CAIRN_ALLOW_DECISION_WRITE") == "1",
			"policy_version": target.Target.PolicyVersion, "model_calls": 0})
		return
	}
	if action == "preview" && request.Method == http.MethodGet {
		query, err := url.ParseQuery(request.URL.RawQuery)
		if err != nil || len(query) < 1 || len(query) > 2 || len(query["preview_key"]) != 1 || !appPolicyKey.MatchString(query.Get("preview_key")) ||
			len(query) == 2 && (len(query["preview_hash"]) != 1 || !appPolicyHash.MatchString(query.Get("preview_hash"))) {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		payload, err := backend.GetPolicyPreview(request.Context(), id, query.Get("preview_key"), query.Get("preview_hash"))
		s.writeAppPolicyResult(writer, payload, err)
		return
	}
	if (action != "preview" && action != "apply") || request.Method != http.MethodPost || request.URL.RawQuery != "" {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var body struct {
		Version int    `json:"version"`
		Key     string `json:"preview_key"`
		Hash    string `json:"preview_hash"`
	}
	mediaType, _, mediaErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, 4097))
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	expectedFields := 2
	if action == "apply" {
		expectedFields++
	}
	if err != nil || len(payload) > 4096 || !utf8.Valid(payload) || json.Unmarshal(payload, &fields) != nil ||
		len(fields) != expectedFields || decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		body.Version != 1 || !appPolicyKey.MatchString(body.Key) || action == "apply" && !appPolicyHash.MatchString(body.Hash) ||
		action == "preview" && fields["preview_hash"] != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	// Recovery precedes target, source, write-flag and model/spec reads. The
	// immutable preview or already-applied receipt is the original intent.
	prior, err := backend.GetPolicyPreview(request.Context(), id, body.Key, body.Hash)
	if action == "preview" {
		if err == nil {
			s.writeAppPolicyResult(writer, prior, nil)
			return
		}
		var api *cairn.APIError
		if !errors.As(err, &api) || api.StatusCode != http.StatusNotFound || api.Code != "preview_not_found" {
			s.writeAppPolicyError(writer, err)
			return
		}
		plan, err := prepareAppPolicy(request.Context(), backend, id)
		if err != nil {
			s.writeAppPolicyError(writer, err)
			return
		}
		value, err := backend.PreparePolicyPreview(request.Context(), id, body.Key, plan)
		s.writeAppPolicyResult(writer, value, err)
		return
	}
	if err != nil {
		s.writeAppPolicyError(writer, err)
		return
	}
	var state struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(prior, &state) != nil {
		writeError(writer, http.StatusServiceUnavailable, "receipt_unavailable")
		return
	}
	if state.Status == "applied" {
		s.writeAppPolicyResult(writer, prior, nil)
		return
	}
	if state.Status != "prepared" || os.Getenv("CAIRN_ALLOW_DECISION_WRITE") != "1" {
		// No receipt does not prove an older ambiguous action never committed.
		writeError(writer, http.StatusServiceUnavailable, "write_disabled_unconfirmed")
		return
	}
	value, err := backend.ApplyPolicyPreview(request.Context(), id, body.Key, body.Hash)
	s.writeAppPolicyResult(writer, value, err)
}

func appPolicyTarget(ctx context.Context, backend appPolicyBackend) (cairn.HandshakeResult, *classify.QuestionSpec, error) {
	policy := classify.DefaultPolicy()
	target, err := backend.Handshake(ctx, cairn.Capabilities{Protocol: "v2", PolicyVersions: []string{policy.Version}})
	if err != nil {
		return target, nil, err
	}
	target.Supported = false
	if target.Target.Protocol != "v2" || target.Target.PolicyVersion != policy.Version {
		return target, nil, nil
	}
	stored, err := backend.GetQuestionSpec(ctx, target.Target.SpecID)
	if err != nil {
		return target, nil, err
	}
	spec, err := classify.DecodeSpec(stored.Payload)
	if err != nil || spec.SpecID != target.Target.SpecID || spec.SemanticHash != target.Target.SpecHash || spec.TaxonomyVersion != target.Target.TaxonomyVersion {
		return target, nil, nil
	}
	// Discovery is read-only. Declare every exact capability only after the
	// immutable spec was decoded locally; never override Worker's supported.
	confirmed, err := backend.Handshake(ctx, cairn.Capabilities{Protocol: "v2", SpecIDs: []string{spec.SpecID},
		TaxonomyVersions: []string{spec.TaxonomyVersion}, PolicyVersions: []string{policy.Version}, Models: []string{target.Target.RequestedModel}})
	if err != nil {
		return confirmed, nil, err
	}
	if confirmed.Target != target.Target {
		confirmed.Supported = false
	}
	if !confirmed.Supported {
		return confirmed, nil, nil
	}
	return confirmed, &spec, nil
}

func prepareAppPolicy(ctx context.Context, backend appPolicyBackend, id int64) (cairn.PolicyReplayRequest, error) {
	refuse := func(code string) (cairn.PolicyReplayRequest, error) {
		return cairn.PolicyReplayRequest{}, &cairn.APIError{StatusCode: http.StatusConflict, Code: code}
	}
	target, spec, err := appPolicyTarget(ctx, backend)
	if err != nil {
		return cairn.PolicyReplayRequest{}, err
	}
	if !target.Supported || spec == nil {
		return refuse("target_policy_unsupported")
	}
	run, err := backend.GetReplayableRun(ctx, id)
	if err != nil {
		return cairn.PolicyReplayRequest{}, err
	}
	if run == nil || run.Status != "succeeded" || run.Coverage != "complete" || run.SpecID != target.Target.SpecID ||
		run.SpecHash != target.Target.SpecHash || run.RequestedModel != target.Target.RequestedModel {
		return refuse("no_replayable_run")
	}
	raw, err := run.DecodeJudgments(*spec)
	if err != nil {
		return refuse("run_not_replayable")
	}
	historical, err := classify.DecodePolicy(run.Policy)
	if err != nil {
		return refuse("historical_policy_missing")
	}
	// This is explicitly the supported deployed policy, never a substituted
	// historical baseline. The original policy above is required for replay.
	next := classify.DefaultPolicy()
	_, after, _, err := classify.Replay(raw, historical, next)
	if err != nil {
		return refuse("run_not_replayable")
	}
	hash, err := classify.PolicyHash(next)
	if err != nil {
		return refuse("policy_invalid")
	}
	view, err := backend.GetV2Selection(ctx, id)
	if err != nil {
		return cairn.PolicyReplayRequest{}, err
	}
	return cairn.PolicyReplayRequest{RunIDs: []int64{run.ID}, PolicyVersion: next.Version, Policy: next, PolicyHash: hash,
		Automatic: classify.AutomaticFromProposals(after), ExpectedRevision: view.Revision, ContentRevision: run.ContentRevision,
		SpecID: run.SpecID, SpecHash: run.SpecHash, RequestedModel: run.RequestedModel, ResolvedModel: run.ResolvedModel,
		ExpectedTargetGeneration: target.Target.Generation}, nil
}

func (s *Server) writeAppPolicyError(writer http.ResponseWriter, err error) {
	var api *cairn.APIError
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusNotImplemented, "policy_replay_unsupported")
	} else if errors.Is(err, cairn.ErrInvalidPolicyPreview) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
	} else if errors.As(err, &api) && (api.StatusCode == 400 || api.StatusCode == 404 || api.StatusCode == 409 || api.StatusCode == 413) {
		writeError(writer, api.StatusCode, api.Code)
	} else {
		writeError(writer, http.StatusServiceUnavailable, "policy_executor_unavailable")
	}
}

func (s *Server) writeAppPolicyResult(writer http.ResponseWriter, payload json.RawMessage, err error) {
	if err != nil {
		s.writeAppPolicyError(writer, err)
		return
	}
	var public map[string]json.RawMessage
	if json.Unmarshal(payload, &public) != nil || public == nil {
		writeError(writer, http.StatusServiceUnavailable, "receipt_unavailable")
		return
	}
	delete(public, "plan")
	delete(public, "review_hash")
	writeJSON(writer, http.StatusOK, public)
}
