package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxModelResponseBytes  = 4 << 20
	maxModelHTTPAttempts   = 3
	modelRetryBaseDelay    = 500 * time.Millisecond
	maxModelRetryDelay     = 5 * time.Second
	slowModelFailure       = 30 * time.Second
	promptTemplate         = "读取此 X 帖及相关评论。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖或相关评论中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"
	postOnlyPromptTemplate = "读取此 X 帖。优先读取原帖正文；不要展开全量评论，只有在评论可立即获得且直接相关时才纳入。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"
	sourcePromptTemplate   = "基于已提供的 X 原文生成增强结果。不要搜索、不要补写未提供的正文。严格返回：约20个简体中文字符的标题；原文语言标识；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅保留原文中明确出现且与内容直接相关的最终链接；image_urls 返回空数组。\nURL: %s\n原文:\n%s"
)

var responsePromptVariants = []responsePrompt{
	{name: "thread", template: promptTemplate},
	{name: "post", template: postOnlyPromptTemplate},
}

type responsePrompt struct {
	name     string
	template string
}

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

// Generate uses trusted source text when present, otherwise x_search and strict structured output.
func (c *ResponsesClient) Generate(ctx context.Context, input Input) (Candidate, error) {
	if strings.TrimSpace(input.SourceText) != "" {
		return c.generateFromSource(ctx, input)
	}

	var lastErr error
	for _, prompt := range responsePromptVariants {
		envelope, err := c.invokeResponse(ctx, input, prompt)
		if err != nil {
			lastErr = err
			if !retryableModelError(err) {
				return Candidate{}, err
			}
			continue
		}
		return c.candidateFromEnvelope(input, envelope, false)
	}
	return Candidate{}, lastErr
}

func (c *ResponsesClient) generateFromSource(ctx context.Context, input Input) (Candidate, error) {
	sourceText := strings.TrimSpace(input.SourceText)
	payload := responseRequest{
		Model: c.model,
		Input: []inputMessage{{
			Role:    "user",
			Content: fmt.Sprintf(sourcePromptTemplate, input.URL, sourceText),
		}},
		MaxOutputTokens: c.maxTokens,
		Text: responseTextConfig{Format: responseFormat{
			Type:   "json_schema",
			Name:   "x_enrichment",
			Strict: true,
			Schema: enrichmentSchema(),
		}},
	}
	envelope, err := c.invokePayload(ctx, input, "source", payload)
	if err != nil {
		return Candidate{}, err
	}
	candidate, err := c.candidateFromEnvelope(input, envelope, true)
	if err != nil {
		return Candidate{}, err
	}
	candidate.Result.OriginalText = sourceText
	if len(candidate.Result.RelatedLinks) == 0 && len(input.RelatedLinks) > 0 {
		candidate.Result.RelatedLinks = append([]string(nil), input.RelatedLinks...)
	}
	candidate.Result.ImageURLs = []string{}
	return candidate, nil
}

func (c *ResponsesClient) invokeResponse(ctx context.Context, input Input, prompt responsePrompt) (responseEnvelope, error) {
	payload := responseRequest{
		Model: c.model,
		Input: []inputMessage{{
			Role:    "user",
			Content: fmt.Sprintf(prompt.template, input.URL),
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
	return c.invokePayload(ctx, input, prompt.name, payload)
}

func (c *ResponsesClient) invokePayload(ctx context.Context, input Input, promptName string, payload responseRequest) (responseEnvelope, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("encode model request: %w", err)
	}

	for requestAttempt := 1; requestAttempt <= maxModelHTTPAttempts; requestAttempt++ {
		request, err := c.newGenerateRequest(ctx, body, input, promptName, requestAttempt)
		if err != nil {
			return responseEnvelope{}, err
		}
		started := time.Now()
		response, err := c.httpClient.Do(request)
		if err != nil {
			return responseEnvelope{}, fmt.Errorf("call model API: %w", err)
		}
		if response.StatusCode == http.StatusOK {
			defer func() { _ = response.Body.Close() }()
			var envelope responseEnvelope
			decoder := json.NewDecoder(io.LimitReader(response.Body, maxModelResponseBytes))
			if err := decoder.Decode(&envelope); err != nil {
				return responseEnvelope{}, fmt.Errorf("decode model response: %w", err)
			}
			return envelope, nil
		}

		modelErr := readModelHTTPError(response)
		shouldRetry := shouldRetryModelRequest(response.StatusCode, requestAttempt, time.Since(started))
		delay := modelRetryDelay(response, requestAttempt)
		_ = response.Body.Close()
		if !shouldRetry {
			return responseEnvelope{}, modelErr
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return responseEnvelope{}, err
		}
	}
	return responseEnvelope{}, errors.New("model request attempts exhausted")
}

func (c *ResponsesClient) candidateFromEnvelope(input Input, envelope responseEnvelope, sourceVerified bool) (Candidate, error) {
	if envelope.Status != "completed" {
		return Candidate{}, fmt.Errorf("model response status is %q", envelope.Status)
	}

	searchVerified := sourceVerified
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
		AITitle          string   `json:"ai_title"`
		OriginalLanguage string   `json:"original_language"`
		OriginalText     string   `json:"original_text"`
		TranslatedText   string   `json:"translated_text"`
		Summary          string   `json:"summary"`
		RelatedLinks     []string `json:"related_links"`
		ImageURLs        []string `json:"image_urls"`
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
			AITitle:          wire.AITitle,
			OriginalLanguage: wire.OriginalLanguage,
			OriginalText:     wire.OriginalText,
			TranslatedText:   wire.TranslatedText,
			Summary:          wire.Summary,
			RelatedLinks:     wire.RelatedLinks,
			ImageURLs:        wire.ImageURLs,
			Model:            model,
		},
		SearchVerified: searchVerified,
	}, nil
}

func (c *ResponsesClient) newGenerateRequest(ctx context.Context, body []byte, input Input, promptName string, requestAttempt int) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create model request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", modelIdempotencyKey(input, promptName, requestAttempt))
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	return request, nil
}

func modelIdempotencyKey(input Input, promptName string, requestAttempt int) string {
	base := fmt.Sprintf("cairn-link-%d-attempt-%d", input.ID, input.Attempt)
	if promptName == "thread" && requestAttempt == 1 {
		return base
	}
	return fmt.Sprintf("%s-%s-%d", base, promptName, requestAttempt)
}

func shouldRetryModelRequest(status, requestAttempt int, elapsed time.Duration) bool {
	return requestAttempt < maxModelHTTPAttempts && retryableModelStatus(status) && elapsed < slowModelFailure
}

func retryableModelError(err error) bool {
	var modelErr *ModelHTTPError
	return errors.As(err, &modelErr) && retryableModelStatus(modelErr.StatusCode)
}

func retryableModelStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func modelRetryDelay(response *http.Response, requestAttempt int) time.Duration {
	if delay, ok := retryAfterDelay(response.Header.Get("Retry-After")); ok {
		return min(delay, maxModelRetryDelay)
	}
	return min(time.Duration(requestAttempt)*modelRetryBaseDelay, maxModelRetryDelay)
}

func retryAfterDelay(raw string) (time.Duration, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
			"ai_title":          map[string]any{"type": "string"},
			"original_language": map[string]any{"type": "string"},
			"original_text":     map[string]any{"type": "string"},
			"translated_text":   map[string]any{"type": "string"},
			"summary":           map[string]any{"type": "string"},
			"related_links": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"image_urls": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required": []string{
			"ai_title", "original_language", "original_text", "translated_text",
			"summary", "related_links", "image_urls",
		},
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
	Tools           []responseTool     `json:"tools,omitempty"`
	ToolChoice      string             `json:"tool_choice,omitempty"`
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
