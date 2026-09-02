package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxModelResponseBytes = 4 << 20
	promptTemplate        = "读取这个 X 链接。返回原文、简短摘要，以及原文或评论中与主题直接相关的最终链接；忽略广告和无关链接。\nURL: %s"
)

// ResponsesClient implements the xAI-specific Responses wire protocol.
type ResponsesClient struct {
	endpoint   string
	apiKey     string
	model      string
	maxTokens  int
	userAgent  string
	httpClient *http.Client
}

// ModelHTTPError reports a non-success status from the model endpoint.
type ModelHTTPError struct {
	StatusCode int
	Message    string
}

func (e *ModelHTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("model API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("model API returned HTTP %d: %s", e.StatusCode, e.Message)
}

// NewResponsesClient creates a narrow xAI Responses API adapter.
func NewResponsesClient(baseURL, apiKey, model string, maxTokens int, userAgent string, httpClient *http.Client) *ResponsesClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ResponsesClient{
		endpoint:   strings.TrimRight(baseURL, "/") + "/responses",
		apiKey:     apiKey,
		model:      model,
		maxTokens:  maxTokens,
		userAgent:  userAgent,
		httpClient: httpClient,
	}
}

// Generate uses x_search and strict structured output to produce a candidate.
func (c *ResponsesClient) Generate(ctx context.Context, input Input) (Candidate, error) {
	payload := responseRequest{
		Model: c.model,
		Input: []inputMessage{{
			Role:    "user",
			Content: fmt.Sprintf(promptTemplate, input.URL),
		}},
		Tools:           []responseTool{{Type: "x_search"}},
		ToolChoice:      "required",
		MaxOutputTokens: c.maxTokens,
		Text: responseTextConfig{Format: responseFormat{
			Type:   "json_schema",
			Name:   "x_enrichment",
			Strict: true,
			Schema: enrichmentSchema(),
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Candidate{}, fmt.Errorf("encode model request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Candidate{}, fmt.Errorf("create model request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", fmt.Sprintf("cairn-link-%d-attempt-%d", input.ID, input.Attempt))
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return Candidate{}, fmt.Errorf("call model API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Candidate{}, readModelHTTPError(response)
	}

	var envelope responseEnvelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxModelResponseBytes))
	if err := decoder.Decode(&envelope); err != nil {
		return Candidate{}, fmt.Errorf("decode model response: %w", err)
	}
	if envelope.Status != "completed" {
		return Candidate{}, fmt.Errorf("model response status is %q", envelope.Status)
	}

	searchVerified := false
	var outputTexts []string
	for _, item := range envelope.Output {
		if item.Status == "completed" && isXSearchOutput(item) {
			searchVerified = true
		}
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				outputTexts = append(outputTexts, content.Text)
			}
		}
	}
	if len(outputTexts) != 1 {
		return Candidate{}, fmt.Errorf("model response contains %d output text blocks, want 1", len(outputTexts))
	}

	var wire struct {
		OriginalText string   `json:"original_text"`
		Summary      string   `json:"summary"`
		RelatedLinks []string `json:"related_links"`
	}
	if err := decodeStrictJSON(strings.NewReader(outputTexts[0]), &wire); err != nil {
		return Candidate{}, fmt.Errorf("decode structured model output: %w", err)
	}
	model := strings.TrimSpace(envelope.Model)
	if model == "" {
		model = c.model
	}
	return Candidate{
		Input: input,
		Result: Result{
			OriginalText: wire.OriginalText,
			Summary:      wire.Summary,
			RelatedLinks: wire.RelatedLinks,
			Model:        model,
		},
		SearchVerified: searchVerified,
	}, nil
}

func isXSearchOutput(item responseOutputItem) bool {
	if item.Type == "x_search_call" {
		return true
	}
	if item.Type != "custom_tool_call" {
		return false
	}
	switch item.Name {
	case "x_thread_fetch", "x_keyword_search", "x_semantic_search", "x_user_search":
		return true
	default:
		return false
	}
}

func enrichmentSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"original_text": map[string]any{"type": "string"},
			"summary":       map[string]any{"type": "string"},
			"related_links": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"original_text", "summary", "related_links"},
		"additionalProperties": false,
	}
}

func decodeStrictJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func readModelHTTPError(response *http.Response) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload)
	message := strings.TrimSpace(payload.Error.Message)
	if len(message) > 500 {
		message = message[:500]
	}
	return &ModelHTTPError{StatusCode: response.StatusCode, Message: message}
}

type responseRequest struct {
	Model           string             `json:"model"`
	Input           []inputMessage     `json:"input"`
	Tools           []responseTool     `json:"tools"`
	ToolChoice      string             `json:"tool_choice"`
	MaxOutputTokens int                `json:"max_output_tokens"`
	Text            responseTextConfig `json:"text"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseTool struct {
	Type string `json:"type"`
}

type responseTextConfig struct {
	Format responseFormat `json:"format"`
}

type responseFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type responseEnvelope struct {
	Status string               `json:"status"`
	Model  string               `json:"model"`
	Output []responseOutputItem `json:"output"`
}

type responseOutputItem struct {
	Type    string                  `json:"type"`
	Name    string                  `json:"name"`
	Status  string                  `json:"status"`
	Content []responseOutputContent `json:"content"`
}

type responseOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
