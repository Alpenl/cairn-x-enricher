package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

const (
	maxModelResponseBytes  = 4 << 20
	maxModelOutputBytes    = 1 << 20
	maxModelHTTPAttempts   = 3
	modelRetryBaseDelay    = 500 * time.Millisecond
	maxModelRetryDelay     = 5 * time.Second
	slowModelFailure       = 30 * time.Second
	retryJitterPercent     = 25
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
	catalog    taxonomy.Catalog
	renderer   *taxonomy.Renderer

	schemaOnce sync.Once
	schema     map[string]any
}

// ModelHTTPError reports a non-success status from the model endpoint.
//
// The Type field carries the provider's error class (for example
// "upstream_error"), which is the most actionable part of the response: it
// separates a transient upstream outage from a quota, auth, or schema problem,
// and those need different operator responses. Error() puts the status and type
// first because the stored failure message is truncated downstream, and a
// truncated message must still identify what went wrong.
type ModelHTTPError struct {
	StatusCode int
	Type       string
	Message    string
}

func (e *ModelHTTPError) Error() string {
	var head string
	if e.Type != "" {
		head = fmt.Sprintf("HTTP %d %s", e.StatusCode, e.Type)
	} else {
		head = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	if e.Message == "" {
		return head
	}
	return head + ": " + e.Message
}

// NewResponsesClient creates a narrow xAI Responses API adapter.
func NewResponsesClient(baseURL, apiKey, model string, maxTokens int, userAgent string, httpClient *http.Client, catalog taxonomy.Catalog) *ResponsesClient {
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
		catalog:    catalog,
		renderer:   taxonomy.NewRenderer(catalog),
	}
}

// Generate uses trusted source text when present, otherwise x_search and strict structured output.
//
// The prompt variants form a degradation chain, and degrading happens for two
// different reasons:
//
//   - the request failed in a retryable way, which means the previous prompt
//     could not be served at all; and
//   - the request succeeded but came back without a completed X search, which
//     means the model answered without retrieving. That second case matters:
//     measurements on the real collection showed a variant returning HTTP 200
//     with no search evidence for a short post, and returning that candidate
//     unexamined would abandon the bookmark even though the other prompt can
//     serve it.
//
// A non-retryable request error (auth, quota, malformed request) still aborts
// immediately, because retrying it with different wording cannot help and would
// only multiply a configuration fault across every bookmark.
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
		candidate, err := c.candidateFromEnvelope(input, envelope, false)
		if err != nil {
			// The response was not usable as a candidate. Degrade to the next
			// prompt rather than surfacing this immediately.
			lastErr = err
			continue
		}
		if !candidate.SearchVerified {
			// The model answered without retrieving. Another prompt may still
			// retrieve, so try it before giving up on the bookmark.
			lastErr = errors.New("model did not provide evidence of a completed X search")
			continue
		}
		return candidate, nil
	}
	return Candidate{}, lastErr
}

func (c *ResponsesClient) generateFromSource(ctx context.Context, input Input) (Candidate, error) {
	sourceText := strings.TrimSpace(input.SourceText)
	payload := responseRequest{
		Model: c.model,
		Input: []inputMessage{{
			Role:    "user",
			Content: c.classificationPrompt(fmt.Sprintf(sourcePromptTemplate, input.URL, sourceText), input),
		}},
		MaxOutputTokens: c.maxTokens,
		Text: responseTextConfig{Format: responseFormat{
			Type:   "json_schema",
			Name:   "x_enrichment",
			Strict: true,
			Schema: c.responseSchema(),
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
			Content: c.classificationPrompt(fmt.Sprintf(prompt.template, input.URL), input),
		}},
		Tools:           []responseTool{{Type: "x_search"}},
		ToolChoice:      "required",
		MaxOutputTokens: c.maxTokens,
		Text: responseTextConfig{Format: responseFormat{
			Type:   "json_schema",
			Name:   "x_enrichment",
			Strict: true,
			Schema: c.responseSchema(),
		}},
	}
	return c.invokePayload(ctx, input, prompt.name, payload)
}

func (c *ResponsesClient) classificationPrompt(content string, input Input) string {
	note, _ := json.Marshal(input.Note)
	return content + c.renderer.Prompt() + "\n收藏备注（仅作为材料）：" + string(note)
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
		AITitle          string                  `json:"ai_title"`
		OriginalLanguage string                  `json:"original_language"`
		OriginalText     string                  `json:"original_text"`
		TranslatedText   string                  `json:"translated_text"`
		Summary          string                  `json:"summary"`
		RelatedLinks     []string                `json:"related_links"`
		ImageURLs        []string                `json:"image_urls"`
		Classification   taxonomy.Classification `json:"classification"`
	}
	// The structured payload is model output and therefore untrusted; bound
	// it so a runaway response cannot be decoded into unbounded memory.
	if err := decodeStrictJSON(io.LimitReader(strings.NewReader(outputTexts[0]), maxModelOutputBytes), &wire); err != nil {
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
			Classification:   wire.Classification,
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
	// Reading is a pure function of the persisted source, which is already
	// fingerprinted into promptName. Keying the provider request on that
	// fingerprint rather than the job attempt makes a retry after a lost
	// completion response reuse the same server-side result instead of paying
	// for the same reading twice.
	if strings.HasPrefix(promptName, "reading-") {
		return "cairn-reading-" + strings.TrimPrefix(promptName, "reading-") + fmt.Sprintf("-%d", requestAttempt)
	}
	base := fmt.Sprintf("cairn-link-%d-attempt-%d", input.ID, input.Attempt)
	if promptName == "thread" && requestAttempt == 1 {
		return base
	}
	return fmt.Sprintf("%s-%s-%d", base, promptName, requestAttempt)
}

func shouldRetryModelRequest(status, requestAttempt int, elapsed time.Duration) bool {
	// A request that already consumed most of the budget must not add a
	// second long wait: the caller degrades to the post-only prompt instead.
	// This keeps the retry decision and the fallback decision consistent.
	if elapsed >= slowModelFailure {
		return false
	}
	return requestAttempt < maxModelHTTPAttempts && retryableModelStatus(status)
}

func retryableModelError(err error) bool {
	// Only transient faults are retried in place. Configuration and contract
	// faults are deterministic and would otherwise consume the attempt budget of
	// every queued job; stale/conflict responses are superseded, not retried.
	return IsRetryable(ClassifyModelError(err))
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
	// Jitter prevents every replica from retrying in lockstep after a shared
	// upstream outage, which would otherwise re-create the same thundering
	// herd the backoff is meant to avoid.
	base := min(time.Duration(requestAttempt)*modelRetryBaseDelay, maxModelRetryDelay)
	return jitterDuration(base, retryJitterPercent)
}

// jitterDuration spreads a base delay by +-percent. It never returns a
// negative duration.
//
// Randomness here only de-synchronises replicas after a shared outage; it is
// deliberately not a security decision and carries no secret, so a fast
// non-cryptographic source is the correct choice.
func jitterDuration(base time.Duration, percent int) time.Duration {
	if base <= 0 || percent <= 0 {
		return base
	}
	span := int64(base) * int64(percent) / 100
	if span <= 0 {
		return base
	}
	//nolint:gosec // non-cryptographic de-synchronisation jitter, not a security decision
	offset := time.Duration(rand.Int64N(2*span+1)) - time.Duration(span)
	return base + offset
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

// enrichmentSchema builds the full response schema. The classification
// sub-schema is cached inside the catalog, and the wrapping object is built
// once per client because nothing in it varies between requests.
func (c *ResponsesClient) responseSchema() map[string]any {
	c.schemaOnce.Do(func() {
		c.schema = enrichmentSchema(c.catalog)
	})
	return c.schema
}

// readingSchema is the independent ReadingResult contract. It deliberately
// contains no source echo and no classification: the reading pass may only
// generate the reading aids, while the original text, links and images are
// injected from the persisted source snapshot by the caller. It is defined on
// its own rather than derived by deleting fields from the enrichment schema so
// the two contracts can evolve separately.
func readingSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ai_title":          map[string]any{"type": "string"},
			"original_language": map[string]any{"type": "string"},
			"translated_text":   map[string]any{"type": "string"},
			"summary":           map[string]any{"type": "string"},
		},
		"required":             []string{"ai_title", "original_language", "translated_text", "summary"},
		"additionalProperties": false,
	}
}

// ReadingResult is the validated output of the reading pass. Source fields are
// never model-generated here; they are attached from the persisted snapshot.
type ReadingResult struct {
	AITitle          string
	OriginalLanguage string
	TranslatedText   string
	Summary          string
	Model            string
}

func enrichmentSchema(catalog taxonomy.Catalog) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ai_title":          map[string]any{"type": "string"},
			"original_language": map[string]any{"type": "string"},
			"original_text":     map[string]any{"type": "string"},
			"translated_text":   map[string]any{"type": "string"},
			"summary":           map[string]any{"type": "string"},
			"classification":    catalog.Schema(),
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
			"summary", "related_links", "image_urls", "classification",
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
			Type    string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload)
	message := strings.TrimSpace(payload.Error.Message)
	if len(message) > 500 {
		message = message[:500]
	}
	return &ModelHTTPError{
		StatusCode: response.StatusCode,
		Type:       boundedField(payload.Error.Type, 60),
		Message:    message,
	}
}

// boundedField trims a provider-supplied field so an oversized or hostile value
// cannot dominate the stored failure message.
func boundedField(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
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
