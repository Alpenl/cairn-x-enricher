// Package ablation runs controlled ablation experiments over the enrichment
// pipeline's design decisions against the real model endpoint.
//
// Each run varies exactly one decision relative to the full system (FULL) and
// records quality signals, latency, and token usage. The harness reuses the
// production client and validator so a variant measures the pipeline the
// service actually runs, not a re-implementation.
package ablation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Sample is one frozen input to every variant. Samples are captured once and
// reused verbatim so variants differ only in the treatment under test.
type Sample struct {
	ID           int64    `json:"id"`
	URL          string   `json:"url"`
	Note         string   `json:"note"`
	SourceText   string   `json:"source_text,omitempty"`
	RelatedLinks []string `json:"related_links,omitempty"`
	// ReferenceText is the human/tool-verified original post text used to score
	// fidelity. Empty means the sample is scored only on structural signals.
	ReferenceText string `json:"reference_text,omitempty"`
}

// Variant is one ablated configuration of the pipeline.
type Variant struct {
	// RequiresSource marks a variant that only applies to samples carrying
	// trusted source text.
	RequiresSource bool
	// Name is the short identifier used in the report.
	Name string
	// Ablates describes which production decision this variant removes.
	Ablates string
	// Run executes the variant and returns its observable outcome.
	Run func(context.Context, *Runner, Sample) Outcome
}

// Outcome is the raw, unjudged result of running one variant on one sample.
type Outcome struct {
	// Title, Summary, Language, OriginalText, TranslatedText, Links, Images are
	// the payload a variant produced.
	Title          string
	Summary        string
	Language       string
	OriginalText   string
	TranslatedText string
	Links          []string
	Images         []string
	Selection      taxonomy.Selection
	Uncertainty    bool
	DiscardedTags  []string

	// SearchEvidence reports whether the response carried a completed X search
	// tool call. FULL requires this; ablations that drop the requirement
	// measure what that requirement costs.
	SearchEvidence bool

	// Usage accounting.
	Latency      time.Duration
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	ModelCalls   int

	// Transport and validation failures.
	Err            string
	ValidationErr  string
	SchemaRejected bool
	// InfraFailed marks a run that never reached model output.
	InfraFailed bool
}

// OK reports whether the variant produced a structurally accepted result.
func (o Outcome) OK() bool { return o.Err == "" && o.ValidationErr == "" }

// Runner holds the endpoint configuration shared by all variants.
type Runner struct {
	Endpoint string
	APIKey   string
	Model    string
	MaxTok   int
	Catalog  taxonomy.Catalog
	Client   *http.Client

	mu    sync.Mutex
	calls int
}

// EnvFileValue reads KEY=VALUE from a dotenv-style file.
//
// The path comes from an operator-supplied flag, never from network input.
func EnvFileValue(path, key string) string {
	//nolint:gosec // path is an explicit CLI argument in a local experiment tool
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == key {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

// maxModelAttempts bounds retries so a permanent provider fault surfaces as a
// failed observation rather than an unbounded experiment. The endpoint used
// for this study returns transient 502s under concurrent load, so the budget
// is deliberately larger than the production client's three: an infrastructure
// blip must not be scored as a quality regression.
const maxModelAttempts = 6

// InfraFailure marks an observation that never produced model output because
// the transport or upstream failed. Such runs are excluded from quality
// aggregation; otherwise a flaky endpoint would look like a bad variant.
type InfraFailure struct{ Reason string }

func (e *InfraFailure) Error() string { return e.Reason }

// usage mirrors the token accounting the provider returns.
type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type outputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type outputItem struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Content []outputContent `json:"content"`
}

// Envelope is one decoded Responses API reply. It is exported because
// Runner.Call returns it to sibling experiment commands.
type Envelope struct {
	Status string       `json:"status"`
	Model  string       `json:"model"`
	Output []outputItem `json:"output"`
	Usage  usage        `json:"usage"`
}

// Request is one low-level Responses API call. Variants use it directly so
// they can vary the payload shape (tools, schema, prompt) independently.
type Request struct {
	Content    string
	Tools      bool
	ToolChoice string
	Schema     map[string]any
	MaxTok     int
	// SourcePath marks the trusted-source recovery path, which by design does
	// not search. Production treats the supplied text as the evidence on that
	// path (generateFromSource passes sourceVerified=true), so requiring an
	// observed X search here would reject every correct recovery result.
	SourcePath bool
}

// Call performs one request and returns the parsed envelope. It retries the
// retryable upstream statuses with a backoff that is generous because this
// endpoint is shared: a variant must not be scored on a transient provider
// failure.
func (r *Runner) Call(ctx context.Context, req Request) (Envelope, error) {
	var lastErr error
	for attempt := 1; attempt <= maxModelAttempts; attempt++ {
		env, retryable, err := r.callOnce(ctx, req)
		if err == nil {
			return env, nil
		}
		lastErr = err
		if !retryable || attempt == maxModelAttempts {
			break
		}
		// 2s, 4s, 8s, 16s, 32s. Capped so a long outage still terminates.
		delay := time.Duration(2<<(attempt-1)) * time.Second
		if delay > 32*time.Second {
			delay = 32 * time.Second
		}
		select {
		case <-ctx.Done():
			return Envelope{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if isTransportFailure(lastErr) {
		return Envelope{}, &InfraFailure{Reason: lastErr.Error()}
	}
	return Envelope{}, lastErr
}

// isTransportFailure reports whether the final error was upstream
// infrastructure rather than a response the model actually produced.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504", "HTTP 408", "HTTP 429",
		"call model:", "read model response:", "context deadline exceeded",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// callOnce issues a single request; the bool reports whether the failure was
// retryable.
func (r *Runner) callOnce(ctx context.Context, req Request) (Envelope, bool, error) {
	payload := map[string]any{
		"model":             r.Model,
		"input":             []map[string]any{{"role": "user", "content": req.Content}},
		"max_output_tokens": req.MaxTok,
	}
	if req.Tools {
		payload["tools"] = []map[string]any{{"type": "x_search"}}
	}
	if req.ToolChoice != "" {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Schema != nil {
		payload["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "x_enrichment", "strict": true, "schema": req.Schema,
		}}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, false, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint, strings.NewReader(string(body)))
	if err != nil {
		return Envelope{}, false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+r.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// A stable per-request key keeps retries idempotent without colliding
	// across variants, which would otherwise let the provider dedupe them.
	httpReq.Header.Set("Idempotency-Key", r.key(req.Content))

	client := r.Client
	if client == nil {
		client = &httpClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Envelope{}, true, fmt.Errorf("call model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Envelope{}, true, fmt.Errorf("read model response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Envelope{}, retryableStatus(resp.StatusCode),
			fmt.Errorf("model HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, false, fmt.Errorf("decode envelope: %w", err)
	}
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return env, false, nil
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (r *Runner) key(content string) string {
	sum := sha256.Sum256([]byte(r.Model + "|" + content))
	return "ablation-" + hex.EncodeToString(sum[:8])
}

// Calls returns how many model requests the runner has issued.
func (r *Runner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// structuredPayload extracts the single structured text block and the search
// evidence flag from an envelope, mirroring the production parser.
func structuredPayload(env Envelope) (string, bool, error) {
	if env.Status != "completed" {
		return "", false, fmt.Errorf("status %q", env.Status)
	}
	var texts []string
	search := false
	for _, item := range env.Output {
		if item.Status == "completed" && isSearchOutput(item) {
			search = true
		}
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				texts = append(texts, c.Text)
			}
		}
	}
	if len(texts) != 1 {
		return "", search, fmt.Errorf("got %d output text blocks, want 1", len(texts))
	}
	return texts[0], search, nil
}

func isSearchOutput(item outputItem) bool {
	if item.Type == "x_search_call" {
		return true
	}
	if item.Type != "custom_tool_call" {
		return false
	}
	switch item.Name {
	case "x_thread_fetch", "x_keyword_search", "x_semantic_search", "x_user_search":
		return true
	}
	return false
}

// wireResult is the untrusted structured payload shape.
type wireResult struct {
	AITitle          string                  `json:"ai_title"`
	OriginalLanguage string                  `json:"original_language"`
	OriginalText     string                  `json:"original_text"`
	TranslatedText   string                  `json:"translated_text"`
	Summary          string                  `json:"summary"`
	RelatedLinks     []string                `json:"related_links"`
	ImageURLs        []string                `json:"image_urls"`
	Classification   taxonomy.Classification `json:"classification"`
}

// enrichSchema builds the production reply schema with the given field set.
// Omitting fields is how the "no translation" / "no classification" ablations
// remove a capability instead of merely asking the prompt to skip it.
func enrichSchema(catalog taxonomy.Catalog, fields ...string) map[string]any {
	all := map[string]any{
		"ai_title":          map[string]any{"type": "string"},
		"original_language": map[string]any{"type": "string"},
		"original_text":     map[string]any{"type": "string"},
		"translated_text":   map[string]any{"type": "string"},
		"summary":           map[string]any{"type": "string"},
		"classification":    catalog.Schema(),
		"related_links":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"image_urls":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}
	props := map[string]any{}
	required := []string{}
	for _, f := range fields {
		if sub, ok := all[f]; ok {
			props[f] = sub
			required = append(required, f)
		}
	}
	return map[string]any{
		"type": "object", "properties": props, "required": required,
		"additionalProperties": false,
	}
}

// parseWire decodes the structured payload strictly.
func parseWire(text string) (wireResult, error) {
	var out wireResult
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return wireResult{}, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return wireResult{}, errors.New("trailing JSON data")
	}
	return out, nil
}

// SortedLinks returns links in a stable order for diffing.
func SortedLinks(links []string) []string {
	out := append([]string(nil), links...)
	sort.Strings(out)
	return out
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

// httpClient is the shared transport for all experiment requests. It is
// deliberately generous: the model endpoint can take tens of seconds when a
// search tool is required.
var httpClient = http.Client{Timeout: 6 * time.Minute}
