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
	"sync"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

const (
	maxModelResponseBytes  = 4 << 20
	maxModelOutputBytes    = 1 << 20
	promptTemplate         = "读取此 X 帖及相关评论。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖或相关评论中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"
	postOnlyPromptTemplate = "读取此 X 帖。优先读取原帖正文；不要展开全量评论，只有在评论可立即获得且直接相关时才纳入。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"
	sourcePromptTemplate   = "基于已提供的 X 原文生成增强结果。不要搜索、不要补写未提供的正文。严格返回：约20个简体中文字符的标题；原文语言标识；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅保留原文中明确出现且与内容直接相关的最终链接；image_urls 返回空数组。\nURL: %s\n原文:\n%s"
)

var responsePromptVariants = []responsePrompt{
	{template: promptTemplate},
	{template: postOnlyPromptTemplate},
}

type responsePrompt struct {
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
// The Type field carries a bounded, validated provider error class. A provider
// message can echo private input or credentials, so neither logs nor the stored
// failure string include it. HTTP status and type remain available for triage.
type ModelHTTPError struct {
	StatusCode int
	Type       string
}

func (e *ModelHTTPError) Error() string {
	var head string
	if safeType := safeProviderType(e.Type); safeType != "" {
		head = fmt.Sprintf("HTTP %d %s", e.StatusCode, safeType)
	} else {
		head = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return head
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
// A request error ends this invocation. Even a transient HTTP failure can
// follow a provider-side execution, and the Responses API does not document
// a guarantee that an idempotency key prevents a second paid call.
func (c *ResponsesClient) Generate(ctx context.Context, input Input) (Candidate, error) {
	if strings.TrimSpace(input.SourceText) != "" {
		return c.generateFromSource(ctx, input)
	}

	var lastErr error
	for _, prompt := range responsePromptVariants {
		envelope, err := c.invokeResponse(ctx, input, prompt)
		if err != nil {
			return Candidate{}, err
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
	envelope, err := c.invokePayload(ctx, payload)
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
	return c.invokePayload(ctx, payload)
}

func (c *ResponsesClient) classificationPrompt(content string, input Input) string {
	note, _ := json.Marshal(input.Note)
	return content + c.renderer.Prompt() + "\n收藏备注（仅作为材料）：" + string(note)
}

func (c *ResponsesClient) invokePayload(ctx context.Context, payload responseRequest) (responseEnvelope, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("encode model request: %w", err)
	}

	request, err := c.newGenerateRequest(ctx, body)
	if err != nil {
		return responseEnvelope{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("call model API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return responseEnvelope{}, readModelHTTPError(response)
	}
	var envelope responseEnvelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxModelResponseBytes))
	if err := decoder.Decode(&envelope); err != nil {
		return responseEnvelope{}, fmt.Errorf("decode model response: %w", err)
	}
	return envelope, nil
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

func (c *ResponsesClient) newGenerateRequest(ctx context.Context, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create model request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	// net/http may retry a replayable POST on a reused connection when either
	// Idempotency-Key header is present. A hidden transport retry would escape
	// explicit accounting of paid attempts, so do not send either header here.
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	return request, nil
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
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&payload)
	return &ModelHTTPError{
		StatusCode: response.StatusCode,
		Type:       safeProviderType(payload.Error.Type),
	}
}

// safeProviderType accepts only compact protocol identifiers. Free text from
// the provider is not safe to include in logs or user-visible failure records.
func safeProviderType(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 60 {
		return ""
	}
	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.'
		if !allowed {
			return ""
		}
	}
	return value
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
