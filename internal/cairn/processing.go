package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"unicode/utf8"
)

// ProcessingCapability marks the bounded App lifecycle contract, not internal management authority.
const ProcessingCapability = "X-Cairn-App-Processing"

// ErrInvalidProcessing is a known local refusal before any network write.
var ErrInvalidProcessing = errors.New("invalid or oversized processing intent")
var processingOperationKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)

// ProcessingIntent is a durable explicit action over an observed lifecycle.
// Fence binds the complete source/queue/human identity; the two numeric fields
// also let clients reject a success receipt for an unrelated input or target.
type ProcessingIntent struct {
	Version                 int     `json:"version"`
	OperationKey            string  `json:"operation_key"`
	Fence                   string  `json:"fence"`
	Action                  string  `json:"action"`
	ExpectedContentRevision int64   `json:"expected_content_revision"`
	TargetGeneration        int64   `json:"target_generation"`
	Text                    *string `json:"text,omitempty"`
}

// Valid checks the closed action vocabulary, source identity and text budget.
func (v ProcessingIntent) Valid() bool {
	if v.Version != 1 || !processingOperationKey.MatchString(v.OperationKey) || !materialHash(v.Fence) ||
		!materialPositive(v.ExpectedContentRevision) || v.TargetGeneration < 0 || v.TargetGeneration > 1<<53-1 {
		return false
	}
	if v.Action == "save_primary" {
		return v.ExpectedContentRevision < 1<<53-1 && v.Text != nil && materialText(*v.Text, 100000) && len(*v.Text) <= 100000
	}
	return (v.Action == "retry_reading" || v.Action == "retry_classification") && v.Text == nil
}

// GetProcessing reads independent states and actual available actions without starting work.
func (c *Client) GetProcessing(ctx context.Context, id int64) (json.RawMessage, error) {
	return c.processing(ctx, id, nil)
}

// SubmitProcessing submits one immutable intent, with no redirect or automatic retry.
func (c *Client) SubmitProcessing(ctx context.Context, id int64, intent ProcessingIntent) (json.RawMessage, error) {
	if !intent.Valid() {
		return nil, ErrInvalidProcessing
	}
	return c.processing(ctx, id, &intent)
}

func (c *Client) processing(ctx context.Context, id int64, intent *ProcessingIntent) (json.RawMessage, error) {
	if !materialPositive(id) {
		return nil, ErrInvalidProcessing
	}
	method := http.MethodGet
	var body io.Reader
	if intent != nil {
		var data bytes.Buffer
		encoder := json.NewEncoder(&data)
		encoder.SetEscapeHTML(false)
		if encoder.Encode(intent) != nil {
			return nil, ErrInvalidProcessing
		}
		payload := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
		if len(payload) > 128<<10 {
			return nil, ErrInvalidProcessing
		}
		method, body = http.MethodPost, bytes.NewReader(payload)
	}
	transport := *c.httpClient
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	client := NewClient(c.baseURL, c.token, &transport)
	response, err := client.doRequest(ctx, method, fmt.Sprintf("/api/v2/links/%d/processing", id), body,
		map[string]string{ProcessingCapability: "1"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	marked := response.Header.Get(ProcessingCapability) == "1"
	if response.StatusCode == 405 || response.StatusCode == 501 || response.StatusCode == 404 && !marked {
		return nil, ErrV2Unsupported
	}
	if !marked {
		return nil, errors.New("processing capability acknowledgement missing")
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil {
		return nil, err
	}
	invalid := errors.New("invalid processing response")
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
	if intent == nil && !validProcessingStatus(payload) || intent != nil && !validProcessingReceipt(payload, *intent) {
		return nil, invalid
	}
	return json.RawMessage(payload), nil
}

func validProcessingStatus(payload []byte) bool {
	var v struct {
		Fence      string `json:"fence"`
		Revision   int64  `json:"content_revision"`
		Generation *int64 `json:"target_generation"`
		Stages     map[string]struct {
			State string `json:"state"`
		} `json:"stages"`
		Actions map[string]struct {
			Allowed *bool           `json:"allowed"`
			Reason  json.RawMessage `json:"blocked_reason"`
			Calls   *int            `json:"model_calls_now"`
			MayCall *bool           `json:"may_call_model_later"`
			Queues  []string        `json:"queues"`
		} `json:"actions"`
		MaxTextBytes int  `json:"max_text_bytes"`
		ModelCalls   *int `json:"model_calls"`
	}
	if json.Unmarshal(payload, &v) != nil || !materialHash(v.Fence) || !materialPositive(v.Revision) || v.Generation == nil ||
		*v.Generation < 0 || *v.Generation > 1<<53-1 || v.MaxTextBytes != 100000 || v.ModelCalls == nil || *v.ModelCalls != 0 {
		return false
	}
	for _, stage := range []string{"source", "reading", "classification", "human"} {
		if !materialText(v.Stages[stage].State, 80) {
			return false
		}
	}
	for _, name := range []string{"save_primary", "retry_reading", "retry_classification"} {
		a, ok := v.Actions[name]
		if !ok || a.Allowed == nil || a.Calls == nil || *a.Calls != 0 || a.MayCall == nil || !*a.MayCall || len(a.Reason) == 0 || len(a.Queues) < 1 || len(a.Queues) > 2 {
			return false
		}
		var reason string
		if string(a.Reason) != "null" && (json.Unmarshal(a.Reason, &reason) != nil || !materialText(reason, 200)) || *a.Allowed != (reason == "") {
			return false
		}
		primaryQueues := len(a.Queues) == 1 && a.Queues[0] == "classification" || len(a.Queues) == 2 && a.Queues[0] == "reading" && a.Queues[1] == "classification"
		if name == "retry_reading" && (len(a.Queues) != 1 || a.Queues[0] != "reading") ||
			name == "retry_classification" && (len(a.Queues) != 1 || a.Queues[0] != "classification") ||
			name == "save_primary" && !primaryQueues {
			return false
		}
	}
	return true
}

func validProcessingReceipt(payload []byte, intent ProcessingIntent) bool {
	var v struct {
		OperationKey string `json:"operation_key"`
		Action       string `json:"action"`
		Status       string `json:"status"`
		Revision     int64  `json:"content_revision"`
		Generation   *int64 `json:"target_generation"`
		Hash         string `json:"source_hash"`
		ClassQueued  *bool  `json:"classification_queued"`
		ReadQueued   *bool  `json:"reading_queued"`
		Replayed     *bool  `json:"replayed"`
		Calls        *int   `json:"model_calls"`
	}
	expectedRevision, expectedStatus := intent.ExpectedContentRevision, "queued"
	if intent.Action == "save_primary" {
		expectedRevision++
		expectedStatus = "source_saved"
	}
	if json.Unmarshal(payload, &v) != nil || v.OperationKey != intent.OperationKey || v.Action != intent.Action || v.Status != expectedStatus ||
		v.Revision != expectedRevision || !materialPositive(v.Revision) || v.Generation == nil || *v.Generation != intent.TargetGeneration ||
		!materialHash(v.Hash) || v.ClassQueued == nil || v.ReadQueued == nil || v.Replayed == nil || v.Calls == nil || *v.Calls != 0 {
		return false
	}
	return *v.ClassQueued == (intent.Action != "retry_reading") &&
		(intent.Action == "save_primary" || *v.ReadQueued == (intent.Action == "retry_reading"))
}
