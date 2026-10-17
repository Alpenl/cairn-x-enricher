package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const manualMaterialCapability = "X-Cairn-App-Manual-Material"

// ErrInvalidManualMaterial is a local, pre-send refusal, never an unknown write.
var ErrInvalidManualMaterial = errors.New("invalid or oversized manual material intent")

// ManualMaterialIntent binds a deliberate human fragment to its observed gap
// and immutable input. Retries must send this same intent and operation key.
type ManualMaterialIntent struct {
	Version                 int    `json:"version"`
	OperationKey            string `json:"operation_key"`
	ExpectedContentRevision int64  `json:"expected_content_revision"`
	EvidenceSnapshotID      int64  `json:"evidence_snapshot_id"`
	SourceHash              string `json:"source_hash"`
	TargetGeneration        int64  `json:"target_generation"`
	Kind                    string `json:"kind"`
	SourceRef               string `json:"source_ref"`
	Text                    string `json:"text"`
}

func materialText(value string, limit int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(utf16.Encode([]rune(value))) <= limit
}
func materialPositive(value int64) bool { return value > 0 && value <= 1<<53-1 }
func materialHash(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(r rune) bool { return (r < '0' || r > '9') && (r < 'a' || r > 'f') }) < 0
}
func materialKind(value string) bool {
	return value == "external_link" || value == "image_text" || value == "truncation"
}

// Valid checks the fixed wire contract and UTF-8 budget before any request.
func (v ManualMaterialIntent) Valid() bool {
	return v.Version == 1 && materialText(v.OperationKey, 200) && materialPositive(v.ExpectedContentRevision) &&
		materialPositive(v.EvidenceSnapshotID) && materialHash(v.SourceHash) && v.TargetGeneration >= 0 && v.TargetGeneration <= 1<<53-1 &&
		materialKind(v.Kind) && materialText(v.SourceRef, 2048) && materialText(v.Text, 100000) && len(v.Text) <= 100000
}

// GetManualMaterial reads only available, source-bound manual material options.
// It never starts enrichment, classification, retrieval, or a provider call.
func (c *Client) GetManualMaterial(ctx context.Context, id int64) (json.RawMessage, error) {
	return c.manualMaterial(ctx, id, nil)
}

// SubmitManualMaterial saves a fragment and queues classification of changed
// input. This call never retries automatically and never follows redirects.
func (c *Client) SubmitManualMaterial(ctx context.Context, id int64, intent ManualMaterialIntent) (json.RawMessage, error) {
	if !intent.Valid() {
		return nil, ErrInvalidManualMaterial
	}
	return c.manualMaterial(ctx, id, &intent)
}

func (c *Client) manualMaterial(ctx context.Context, id int64, intent *ManualMaterialIntent) (json.RawMessage, error) {
	if !materialPositive(id) {
		return nil, errors.New("invalid manual material bookmark")
	}
	method := http.MethodGet
	var body io.Reader
	if intent != nil {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(intent); err != nil {
			return nil, ErrInvalidManualMaterial
		}
		payload := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
		if len(payload) > 128<<10 {
			return nil, ErrInvalidManualMaterial
		}
		method, body = http.MethodPost, bytes.NewReader(payload)
	}
	transport := *c.httpClient
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client := NewClient(c.baseURL, c.token, &transport)
	response, err := client.doRequest(ctx, method, fmt.Sprintf("/api/v2/links/%d/manual-material", id), body,
		map[string]string{manualMaterialCapability: "1"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	supported := response.Header.Get(manualMaterialCapability) == "1"
	if response.StatusCode == 405 || response.StatusCode == 501 || response.StatusCode == 404 && !supported {
		return nil, ErrV2Unsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	if !supported {
		return nil, ErrV2Unsupported
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil {
		return nil, err
	}
	invalid := errors.New("invalid manual material response")
	if len(payload) > 128<<10 || !utf8.Valid(payload) {
		return nil, invalid
	}
	var envelope struct {
		Version int   `json:"version"`
		ID      int64 `json:"id"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.ID != id {
		return nil, invalid
	}
	if envelope.Version != 1 {
		return nil, ErrV2Unsupported
	}
	if intent == nil {
		if !validManualMaterialOptions(payload) {
			return nil, invalid
		}
	} else if !validManualMaterialReceipt(payload, *intent) {
		return nil, invalid
	}
	return json.RawMessage(payload), nil
}

func validManualMaterialOptions(payload []byte) bool {
	var value struct {
		Source        json.RawMessage `json:"source"`
		CanSubmit     *bool           `json:"can_submit"`
		BlockedReason json.RawMessage `json:"blocked_reason"`
		MaxTextBytes  int             `json:"max_text_bytes"`
		Options       []struct {
			Kind, Role string
			SourceRef  string `json:"source_ref"`
		} `json:"options"`
	}
	if json.Unmarshal(payload, &value) != nil || value.CanSubmit == nil || len(value.Source) == 0 || len(value.BlockedReason) == 0 ||
		value.MaxTextBytes != 100000 || value.Options == nil || len(value.Options) > 134 {
		return false
	}
	if string(value.Source) == "null" {
		if *value.CanSubmit {
			return false
		}
	} else {
		var source struct {
			Revision   int64  `json:"content_revision"`
			SnapshotID int64  `json:"evidence_snapshot_id"`
			Hash       string `json:"source_hash"`
			Generation *int64 `json:"target_generation"`
		}
		if json.Unmarshal(value.Source, &source) != nil || !materialPositive(source.Revision) || !materialPositive(source.SnapshotID) ||
			!materialHash(source.Hash) || source.Generation == nil || *source.Generation < 0 || *source.Generation > 1<<53-1 {
			return false
		}
	}
	var reason string
	if string(value.BlockedReason) != "null" && (json.Unmarshal(value.BlockedReason, &reason) != nil || !materialText(reason, 200)) {
		return false
	}
	if *value.CanSubmit != (reason == "" && len(value.Options) > 0) || !*value.CanSubmit && reason == "" {
		return false
	}
	seen := map[string]bool{}
	for _, option := range value.Options {
		key := option.Kind + "\x00" + option.SourceRef
		if !materialKind(option.Kind) || !materialText(option.SourceRef, 2048) || seen[key] {
			return false
		}
		switch option.Role {
		case "author_continuation", "quoted", "external_article", "third_party", "legacy_unknown":
		default:
			return false
		}
		seen[key] = true
	}
	return true
}

func validManualMaterialReceipt(payload []byte, intent ManualMaterialIntent) bool {
	var value struct {
		OperationKey string `json:"operation_key"`
		RequestID    string `json:"request_id"`
		Status       string `json:"status"`
		Revision     int64  `json:"content_revision"`
		SnapshotID   int64  `json:"evidence_snapshot_id"`
		Hash         string `json:"source_hash"`
		Changed      *bool  `json:"changed"`
		Queued       *bool  `json:"classification_queued"`
		Replayed     *bool  `json:"replayed"`
		ModelCalls   *int   `json:"model_calls"`
	}
	if json.Unmarshal(payload, &value) != nil || value.OperationKey != intent.OperationKey || !materialText(value.RequestID, 200) ||
		value.Status != "material_saved" || !materialPositive(value.SnapshotID) || !materialHash(value.Hash) ||
		value.Changed == nil || value.Queued == nil || value.Replayed == nil || value.ModelCalls == nil || *value.ModelCalls != 0 || *value.Changed != *value.Queued {
		return false
	}
	if *value.Changed {
		return value.Revision == intent.ExpectedContentRevision+1 && materialPositive(value.Revision) && value.SnapshotID != intent.EvidenceSnapshotID && value.Hash != intent.SourceHash
	}
	return value.Revision == intent.ExpectedContentRevision && value.SnapshotID == intent.EvidenceSnapshotID && value.Hash == intent.SourceHash
}
