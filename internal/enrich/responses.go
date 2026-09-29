package enrich

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	ledger     PaidAttemptLedger
	logger     *slog.Logger
	catalog    taxonomy.Catalog
	renderer   *taxonomy.Renderer

	schemaOnce sync.Once
	schema     map[string]any
}

// SetPaidAttemptLedger wires the durable Worker budget into every model POST.
// Production installs it before the startup canary or any queue work.
func (c *ResponsesClient) SetPaidAttemptLedger(ledger PaidAttemptLedger) { c.ledger = ledger }

// SetLogger attaches the optional, dynamically controlled diagnostic exporter.
// Provider accounting remains in the Worker ledger when logging is off.
func (c *ResponsesClient) SetLogger(logger *slog.Logger) { c.logger = logger }

func (c *ResponsesClient) logPaidAttempt(ctx context.Context, level slog.Level,
	event, stage, variant string, started time.Time, extra ...slog.Attr) {
	if c.ledger == nil || c.logger == nil || !c.logger.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{
		slog.Int("schema_version", 1), slog.String("event_name", event),
		slog.String("stage", stage), slog.String("provider_variant", variant),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
	}
	c.logger.LogAttrs(ctx, level, "provider attempt", append(attrs, extra...)...)
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
	stage, variant := "legacy", "source"
	if input.Canary {
		stage, variant = "canary", "canary"
	}
	envelope, _, err := c.invokePayload(ctx, input, stage, variant, 1, payload)
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
	envelope, _, err := c.invokePayload(ctx, input, "legacy", "legacy", 1, payload)
	return envelope, err
}

func (c *ResponsesClient) classificationPrompt(content string, input Input) string {
	note, _ := json.Marshal(input.Note)
	return content + c.renderer.Prompt() + "\n收藏备注（仅作为材料）：" + string(note)
}

func (c *ResponsesClient) invokePayload(ctx context.Context, input Input, stage, variant string,
	attemptNumber int, payload responseRequest) (responseEnvelope, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return responseEnvelope{}, "", fmt.Errorf("encode model request: %w", err)
	}

	request, err := c.newGenerateRequest(ctx, body)
	if err != nil {
		return responseEnvelope{}, "", err
	}
	var operationKey string
	if c.ledger != nil {
		if stage != "canary" && (input.ID < 1 || input.LeaseToken == "" ||
			input.ContentRevision < 1 || input.MinRemainingMS < 1 || stage == "legacy") {
			return responseEnvelope{}, "", errors.New("paid model attempt lacks a valid leased operation")
		}
		requestDigest := sha256.Sum256(body)
		requestHash := hex.EncodeToString(requestDigest[:])
		if stage == "canary" {
			var nonce [32]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return responseEnvelope{}, "", fmt.Errorf("create canary operation: %w", err)
			}
			operationKey = hex.EncodeToString(nonce[:])
		} else {
			keyDigest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d:%s:%s:%d:%s",
				input.ID, input.LeaseToken, input.ContentRevision, stage, variant, attemptNumber, requestHash)))
			operationKey = hex.EncodeToString(keyDigest[:])
		}
		reservation := ProviderAttempt{OperationKey: operationKey, RequestHash: requestHash,
			Model: c.model, Stage: stage, Variant: variant, AttemptNumber: attemptNumber}
		if stage != "canary" {
			reservation.LinkID = input.ID
			reservation.LeaseToken = input.LeaseToken
			reservation.ContentRevision = input.ContentRevision
			reservation.MinRemainingMS = input.MinRemainingMS
		}
		reserveStarted := time.Now()
		granted, err := c.ledger.ReserveProviderAttempt(ctx, reservation)
		if err != nil {
			c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_denied", stage, variant,
				reserveStarted, slog.String("error_class", string(ClassOf(err))))
			return responseEnvelope{}, "", fmt.Errorf("reserve paid model attempt: %w", err)
		}
		if !granted {
			c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_denied", stage, variant,
				reserveStarted, slog.String("provider_reason", "already_reserved"))
			return responseEnvelope{}, "", errors.New("paid model attempt was already reserved or budget exhausted")
		}
		c.logPaidAttempt(ctx, slog.LevelInfo, "provider_attempt_reserved", stage, variant, reserveStarted)
	}
	providerStarted := time.Now()
	// This records entry into the HTTP transport, not proof that the provider
	// received the request. A transport failure can still have executed remotely.
	c.logPaidAttempt(ctx, slog.LevelInfo, "provider_attempt_dispatching", stage, variant, providerStarted)
	response, err := c.httpClient.Do(request)
	if err != nil {
		c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_unknown", stage, variant,
			providerStarted, slog.String("provider_reason", "network_unknown"))
		return responseEnvelope{}, operationKey, fmt.Errorf("call model API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	// Headers arrived, but the body and the durable settlement are still pending.
	c.logPaidAttempt(ctx, slog.LevelInfo, "provider_response_headers_received", stage, variant,
		providerStarted, slog.Int("provider_http_status", response.StatusCode))
	if response.StatusCode != http.StatusOK {
		if err := c.settleAttempt(ctx, ProviderSettlement{OperationKey: operationKey,
			HTTPStatus: response.StatusCode}); err != nil {
			c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_unknown", stage, variant,
				providerStarted, slog.Int("provider_http_status", response.StatusCode),
				slog.String("provider_reason", "settlement_failed"))
			return responseEnvelope{}, operationKey, err
		}
		c.logPaidAttempt(ctx, slog.LevelInfo, "provider_attempt_responded", stage, variant,
			providerStarted, slog.Int("provider_http_status", response.StatusCode))
		return responseEnvelope{}, operationKey, readModelHTTPError(response)
	}
	var envelope responseEnvelope
	if err := decodeModelEnvelope(response.Body, &envelope); err != nil {
		c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_unknown", stage, variant,
			providerStarted, slog.Int("provider_http_status", response.StatusCode),
			slog.String("provider_reason", "decode_failed"))
		return responseEnvelope{}, operationKey, fmt.Errorf("decode model response: %w", err)
	}
	settlement := ProviderSettlement{OperationKey: operationKey, HTTPStatus: response.StatusCode,
		InputTokens: envelope.Usage.InputTokens, OutputTokens: envelope.Usage.OutputTokens,
		TotalTokens: envelope.Usage.TotalTokens, XSearchCalls: envelope.Usage.ServerSideToolUsage.XSearchCalls,
		CostUSDTicks: envelope.Usage.CostUSDTicks}
	if envelope.ID != "" {
		settlement.ResponseID = &envelope.ID
	}
	if err := c.settleAttempt(ctx, settlement); err != nil {
		c.logPaidAttempt(ctx, slog.LevelWarn, "provider_attempt_unknown", stage, variant,
			providerStarted, slog.Int("provider_http_status", response.StatusCode),
			slog.String("provider_reason", "settlement_failed"))
		return responseEnvelope{}, operationKey, err
	}
	if c.ledger != nil && c.logger != nil && c.logger.Enabled(ctx, slog.LevelInfo) {
		attrs := []slog.Attr{slog.Int("provider_http_status", response.StatusCode)}
		if settlement.InputTokens != nil {
			attrs = append(attrs, slog.Int64("input_tokens", *settlement.InputTokens))
		}
		if settlement.OutputTokens != nil {
			attrs = append(attrs, slog.Int64("output_tokens", *settlement.OutputTokens))
		}
		if settlement.TotalTokens != nil {
			attrs = append(attrs, slog.Int64("total_tokens", *settlement.TotalTokens))
		}
		if settlement.XSearchCalls != nil {
			attrs = append(attrs, slog.Int64("x_search_calls", *settlement.XSearchCalls))
		}
		if settlement.CostUSDTicks != nil {
			attrs = append(attrs, slog.Int64("cost_usd_ticks", *settlement.CostUSDTicks))
		}
		c.logPaidAttempt(ctx, slog.LevelInfo, "provider_attempt_responded", stage, variant, providerStarted, attrs...)
	}
	return envelope, operationKey, nil
}

func (c *ResponsesClient) settleAttempt(ctx context.Context, settlement ProviderSettlement) error {
	if c.ledger == nil {
		return nil
	}
	// A cancelled model deadline must not prevent recording a response we did
	// receive. The Worker operation is idempotent and retains a bounded timeout.
	settleContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := c.ledger.SettleProviderAttempt(settleContext, settlement); err != nil {
		return fmt.Errorf("settle paid model attempt: %w", err)
	}
	return nil
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
	if err := decodeBoundedModelJSON(outputTexts[0], &wire); err != nil {
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

// A LimitReader alone can make a valid JSON prefix look complete while hiding
// an oversized suffix. Check the actual length before decoding model text.
func decodeBoundedModelJSON(text string, target any) error {
	if len(text) > maxModelOutputBytes {
		return fmt.Errorf("model output exceeds %d bytes", maxModelOutputBytes)
	}
	if !utf8.ValidString(text) {
		return errors.New("model output is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(strings.NewReader(text)); err != nil {
		return err
	}
	return decodeStrictJSON(strings.NewReader(text), target)
}

func rejectDuplicateJSONKeys(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("model JSON nesting is too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func decodeModelEnvelope(reader io.Reader, target *responseEnvelope) error {
	body, err := io.ReadAll(io.LimitReader(reader, maxModelResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxModelResponseBytes {
		return fmt.Errorf("model response exceeds %d bytes", maxModelResponseBytes)
	}
	if !utf8.Valid(body) {
		return errors.New("model response is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(bytes.NewReader(body)); err != nil {
		return err
	}
	// Unmarshal rejects a second JSON value or non-whitespace bytes after the
	// envelope; a streaming single Decode would accept either.
	return json.Unmarshal(body, target)
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
	ID     string               `json:"id"`
	Status string               `json:"status"`
	Model  string               `json:"model"`
	Output []responseOutputItem `json:"output"`
	Usage  struct {
		InputTokens         *int64 `json:"input_tokens"`
		OutputTokens        *int64 `json:"output_tokens"`
		TotalTokens         *int64 `json:"total_tokens"`
		CostUSDTicks        *int64 `json:"cost_in_usd_ticks"`
		ServerSideToolUsage struct {
			XSearchCalls *int64 `json:"x_search_calls"`
		} `json:"server_side_tool_usage_details"`
	} `json:"usage"`
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
