package classify

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ProviderCall describes one actual HTTP attempt. It excludes credentials and
// carries only this run's usage; reusing answers does not bill old usage again.
type ProviderCall struct {
	RequestHash    string          `json:"request_hash"`
	StateHash      string          `json:"state_hash"`
	QuestionIDs    []string        `json:"question_ids"`
	RequestedModel string          `json:"requested_model"`
	ResolvedModel  string          `json:"resolved_model,omitempty"`
	Usage          json.RawMessage `json:"usage,omitempty"`
	UsageMissing   bool            `json:"usage_missing"`
	HTTPStatus     int             `json:"http_status"`
	LatencyMS      int64           `json:"latency_ms"`
}

// ValidateReplayMetadata validates an externally loaded evaluation using the
// same immutable-spec, typed-answer and provenance checks as the store reader.
// It makes no inference and rejects legacy records without actual wire state.
func ValidateReplayMetadata(spec QuestionSpec, raw RawJudgments) error {
	if raw.MetadataVersion != 1 || raw.Coverage != "complete" {
		return errors.New("offline replay requires complete versioned evaluation metadata")
	}
	for _, judgment := range raw.Judgments {
		if judgment.Kind == QuestionScore && judgment.Score == nil {
			return errors.New("offline replay score is missing its value")
		}
	}
	answers, err := json.Marshal(answersFromJudgments(raw))
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	restored, err := RestoreStoredJudgments(spec, raw.RequestedModel, raw.ResolvedModel, answers, raw.Coverage, metadata)
	if err != nil {
		return err
	}
	if restored.TaxonomyVersion != spec.TaxonomyVersion {
		return errors.New("offline replay taxonomy identity mismatch")
	}
	return nil
}

func callIdentity(body []byte) (ProviderCall, string, error) {
	var wire providerRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return ProviderCall{}, "", err
	}
	ids := make([]string, 0, len(wire.Questions))
	for id := range wire.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ProviderCall{RequestHash: sha256Hex(body), StateHash: sha256Hex(wire.State), QuestionIDs: ids, RequestedModel: wire.Model, UsageMissing: true}, string(wire.State), nil
}

// RestoreStoredJudgments validates metadata against the immutable spec and
// separately stored typed answers. Legacy absence stays unknown; it does not
// fabricate the old wire state, question hashes or batching identity.
func RestoreStoredJudgments(spec QuestionSpec, requestedModel, resolvedModel string, answersJSON []byte, coverage string, metadata []byte) (RawJudgments, error) {
	base, err := DecodeStoredJudgments(spec, requestedModel, resolvedModel, answersJSON, coverage)
	if err != nil {
		return RawJudgments{}, err
	}
	if len(metadata) == 0 || string(metadata) == "null" {
		return base, nil
	}
	var raw RawJudgments
	if err := rejectDuplicateKeys(metadata); err != nil {
		return RawJudgments{}, err
	}
	if err := strictDecode(metadata, &raw); err != nil {
		return RawJudgments{}, err
	}
	if raw.MetadataVersion == 0 {
		return base, nil
	}
	if raw.MetadataVersion != 1 || raw.SpecID != base.SpecID || raw.SpecHash != base.SpecHash || raw.RequestedModel != requestedModel || raw.ResolvedModel != resolvedModel || raw.Coverage != coverage || raw.BatchSemantics == "" || raw.WireState == "" || sha256Hex([]byte(raw.WireState)) != raw.EvidenceHash {
		return RawJudgments{}, errors.New("stored evaluation identity mismatch")
	}
	// A complete stored run may be replayed only if both representations agree.
	if !reflect.DeepEqual(raw.Judgments, base.Judgments) {
		return RawJudgments{}, errors.New("stored judgment metadata disagrees with answers/spec")
	}
	if len(raw.QuestionHashes) != len(raw.Judgments) {
		return RawJudgments{}, errors.New("stored question identities missing")
	}
	for _, question := range spec.Questions {
		hash, err := QuestionHash(question)
		if err != nil {
			return RawJudgments{}, err
		}
		if raw.QuestionHashes[question.ID] != hash {
			return RawJudgments{}, fmt.Errorf("stored question %s identity mismatch", question.ID)
		}
	}
	var state struct {
		Primary   string          `json:"primary"`
		Context   []EvidenceBlock `json:"context"`
		Coverage  string          `json:"coverage"`
		Truncated bool            `json:"truncated"`
	}
	if err := strictDecode([]byte(raw.WireState), &state); err != nil {
		return RawJudgments{}, fmt.Errorf("stored wire state: %w", err)
	}
	if state.Coverage != raw.EvidenceCoverage || state.Truncated != raw.Truncated || (raw.Coverage == "complete" && len(raw.Missing) != 0) {
		return RawJudgments{}, errors.New("stored coverage differs from actual state/answers")
	}
	if state.Primary == "" {
		return RawJudgments{}, errors.New("stored wire state has no primary evidence")
	}
	seen := map[string]bool{}
	for _, call := range raw.Calls {
		if call.StateHash != raw.EvidenceHash || call.RequestedModel != requestedModel || call.ResolvedModel != resolvedModel || call.HTTPStatus != 200 || !validHash(call.RequestHash) || len(call.QuestionIDs) == 0 {
			return RawJudgments{}, errors.New("stored provider call identity mismatch")
		}
		for _, id := range call.QuestionIDs {
			if _, ok := raw.Judgments[id]; !ok || seen[id] {
				return RawJudgments{}, errors.New("stored provider questions overlap or are unknown")
			}
			seen[id] = true
		}
	}
	for _, id := range raw.Reused {
		if _, ok := raw.Judgments[id]; !ok || seen[id] || raw.ReusedFrom[id] <= 0 {
			return RawJudgments{}, errors.New("stored reuse source missing or overlapping")
		}
		seen[id] = true
	}
	if len(seen) != len(raw.Judgments) || len(raw.ReusedFrom) != len(raw.Reused) {
		return RawJudgments{}, errors.New("stored answer provenance incomplete")
	}
	var input, output int64
	missing := false
	for _, call := range raw.Calls {
		i, o, ok := tokenUsage(call.Usage)
		if call.UsageMissing == ok {
			return RawJudgments{}, errors.New("stored call usage status disagrees with telemetry")
		}
		missing = missing || !ok
		if ok {
			input += i
			output += o
		}
	}
	if missing != raw.UsageMissing {
		return RawJudgments{}, errors.New("stored aggregate usage status mismatch")
	}
	if !missing {
		i, o, ok := tokenUsage(raw.Usage)
		if !ok || i != input || o != output {
			return RawJudgments{}, errors.New("stored aggregate usage differs from new calls")
		}
	}
	return raw, nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func tokenUsage(value json.RawMessage) (int64, int64, bool) {
	var usage struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	}
	if json.Unmarshal(value, &usage) != nil || usage.Input == nil || usage.Output == nil || *usage.Input < 0 || *usage.Output < 0 {
		return 0, 0, false
	}
	return *usage.Input, *usage.Output, true
}
