package ablation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// fullPrompt is the production search prompt (thread variant).
const fullPrompt = "读取此 X 帖及相关评论。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖或相关评论中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"

// allFields is the production reply schema field set.
var allFields = []string{
	"ai_title", "original_language", "original_text", "translated_text",
	"summary", "related_links", "image_urls", "classification",
}

// Variants returns the ablation matrix. Order matters only for reporting.
func Variants() []Variant {
	return []Variant{
		{
			Name:    "FULL",
			Ablates: "none — the production pipeline",
			Run:     runFull,
		},
		{
			Name:    "no_x_search",
			Ablates: "server-side x_search tool and tool_choice=required",
			Run:     runNoSearch,
		},
		{
			Name:    "no_thread_context",
			Ablates: "thread comment retrieval (post-only prompt)",
			Run:     runPostOnly,
		},
		{
			Name:    "no_strict_schema",
			Ablates: "strict JSON Schema constrained decoding",
			Run:     runNoSchema,
		},
		{
			Name:    "no_translation",
			Ablates: "simplified-Chinese translation field",
			Run:     runNoTranslation,
		},
		{
			Name:    "no_classification",
			Ablates: "taxonomy classification, why_suggestion and entities",
			Run:     runNoClassification,
		},
		{
			Name:    "no_search_evidence_gate",
			Ablates: "the requirement that a completed X search be observed",
			Run:     runNoEvidenceGate,
		},
		{
			Name:    "unconstrained_title",
			Ablates: "the Chinese/title-length validator",
			Run:     runUnconstrainedTitle,
		},
	}
}

// baseContent builds the production prompt with the note appended, exactly as
// the service does, so every variant shares prompt scaffolding.
func baseContent(r *Runner, s Sample, prompt string) string {
	content := strings.Replace(prompt, "%s", s.URL, 1)
	// The service JSON-encodes the note so a note containing quotes or newlines
	// cannot break out of the prompt's data section. Reproduce that here.
	note, _ := json.Marshal(s.Note)
	return content + taxonomy.NewRenderer(r.Catalog).Prompt() + "\n收藏备注（仅作为材料）：" + string(note)
}

func runFull(ctx context.Context, r *Runner, s Sample) Outcome {
	return searchVariant(ctx, r, s, fullPrompt, allFields, true)
}

func runNoSearch(ctx context.Context, r *Runner, s Sample) Outcome {
	// Remove the tool entirely: the model must answer from parametric memory.
	prompt := strings.Replace(fullPrompt, "读取此 X 帖及相关评论。", "根据你对以下 X 帖的记忆回答。", 1)
	return generate(ctx, r, s, Request{
		Content: baseContent(r, s, prompt),
		Tools:   false,
		Schema:  enrichSchema(r.Catalog, allFields...),
		MaxTok:  r.MaxTok,
	}, true)
}

func runPostOnly(ctx context.Context, r *Runner, s Sample) Outcome {
	const postOnly = "读取此 X 帖。优先读取原帖正文；不要展开全量评论，只有在评论可立即获得且直接相关时才纳入。严格返回：约20个简体中文字符的标题；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅与内容直接相关的最终链接；原帖中的图片原始媒体 URL（仅 pbs.twimg.com/media）。无图或无链接返回空数组，忽略广告和无关项。\nURL: %s"
	return searchVariant(ctx, r, s, postOnly, allFields, true)
}

func runNoSchema(ctx context.Context, r *Runner, s Sample) Outcome {
	// Keep the search tool and the schema's semantics in the prompt, but let
	// the model emit free-form text. This isolates constrained decoding.
	prompt := fullPrompt + "\n只输出一个 JSON 对象，不要包含任何解释或代码块标记。"
	return generate(ctx, r, s, Request{
		Content:    baseContent(r, s, prompt),
		Tools:      true,
		ToolChoice: "required",
		Schema:     nil,
		MaxTok:     r.MaxTok,
	}, true)
}

func runNoTranslation(ctx context.Context, r *Runner, s Sample) Outcome {
	fields := without(allFields, "translated_text")
	prompt := strings.Replace(fullPrompt, "完整简体中文译文；", "", 1)
	return searchVariant(ctx, r, s, prompt, fields, true)
}

func runNoClassification(ctx context.Context, r *Runner, s Sample) Outcome {
	fields := without(allFields, "classification")
	return searchVariant(ctx, r, s, fullPrompt, fields, false)
}

func runNoEvidenceGate(ctx context.Context, r *Runner, s Sample) Outcome {
	// Same request as FULL; only the acceptance gate is relaxed, which is
	// implemented by the scorer reading SearchEvidence. Marked here so the
	// report can attribute the difference to the gate rather than the request.
	out := searchVariant(ctx, r, s, fullPrompt, allFields, true)
	out.ValidationErr = ""
	return out
}

func runUnconstrainedTitle(ctx context.Context, r *Runner, s Sample) Outcome {
	prompt := strings.Replace(fullPrompt, "约20个简体中文字符的标题", "简洁的标题（可用任意语言）", 1)
	return searchVariant(ctx, r, s, prompt, allFields, true)
}

// searchVariant runs the production search payload shape with overridable
// prompt and schema fields.
func searchVariant(ctx context.Context, r *Runner, s Sample, prompt string, fields []string, withClassification bool) Outcome {
	return generate(ctx, r, s, Request{
		Content:    baseContent(r, s, prompt),
		Tools:      true,
		ToolChoice: "required",
		Schema:     enrichSchema(r.Catalog, fields...),
		MaxTok:     r.MaxTok,
	}, withClassification)
}

// generate issues one request and maps the envelope onto an Outcome,
// reproducing the production validation rules.
func generate(ctx context.Context, r *Runner, _ Sample, req Request, expectClassification bool) Outcome {
	started := time.Now()
	env, err := r.Call(ctx, req)
	out := Outcome{ModelCalls: 1, Latency: time.Since(started)}
	if err != nil {
		var infra *InfraFailure
		if errors.As(err, &infra) {
			out.InfraFailed = true
		}
		out.Err = err.Error()
		return out
	}
	out.InputTokens = env.Usage.InputTokens
	out.OutputTokens = env.Usage.OutputTokens
	out.TotalTokens = env.Usage.TotalTokens

	text, search, err := structuredPayload(env)
	// On the source path, the trusted text is the evidence: the post text came
	// from the caller, not from the model, so no search is expected or needed.
	out.SearchEvidence = search || req.SourcePath
	if err != nil {
		out.ValidationErr = err.Error()
		return out
	}
	wire, err := parseWire(text)
	if err != nil {
		out.ValidationErr = "decode structured output: " + err.Error()
		out.SchemaRejected = req.Schema == nil
		return out
	}

	out.Title = strings.TrimSpace(wire.AITitle)
	out.Summary = strings.TrimSpace(wire.Summary)
	out.Language = strings.TrimSpace(wire.OriginalLanguage)
	out.OriginalText = strings.TrimSpace(wire.OriginalText)
	out.TranslatedText = strings.TrimSpace(wire.TranslatedText)
	out.Links = wire.RelatedLinks
	out.Images = wire.ImageURLs

	if expectClassification {
		norm := r.Catalog.Normalize(wire.Classification)
		out.Selection = norm.Selection
		out.Uncertainty = norm.Uncertainty
		out.DiscardedTags = norm.DiscardedTags
	}

	// Production acceptance rules.
	if err := validateOutcome(out); err != nil {
		out.ValidationErr = err.Error()
	}
	return out
}

// validateOutcome mirrors internal/enrich/validate.go's acceptance rules.
//
// The order matters as much as the rules: production rejects a candidate with
// no completed X search before it looks at any field, because that is the check
// that distinguishes a retrieval failure from a formatting failure. An earlier
// version of this function omitted the evidence check and reported "ai_title
// length out of range" for a run that had actually returned no search evidence,
// which is a misattribution of exactly the kind this study criticises.
func validateOutcome(out Outcome) error {
	if !out.SearchEvidence {
		return errString("model did not provide evidence of a completed X search")
	}
	if out.Title == "" {
		return errString("ai_title missing")
	}
	if !containsHan(out.Title) {
		return errString("ai_title has no Chinese")
	}
	n := len([]rune(out.Title))
	if n < 8 || n > 32 {
		return errString("ai_title length out of range")
	}
	if out.Language == "" {
		return errString("original_language missing")
	}
	if out.OriginalText == "" {
		return errString("original_text missing")
	}
	if out.Summary == "" {
		return errString("summary missing")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

func without(fields []string, drop string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != drop {
			out = append(out, f)
		}
	}
	return out
}

func containsHan(value string) bool {
	for _, r := range value {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// SourceVariant runs the trusted-source path, which the production pipeline
// uses to recover a failed bookmark from already-stored text. Ablating the
// search tool is free here, so it is the correct baseline for cost.
func SourceVariant() Variant {
	return Variant{
		Name:           "source_only",
		Ablates:        "x_search replaced by already-stored original text",
		RequiresSource: true,
		Run: func(ctx context.Context, r *Runner, s Sample) Outcome {
			const tpl = "基于已提供的 X 原文生成增强结果。不要搜索、不要补写未提供的正文。严格返回：约20个简体中文字符的标题；原文语言标识；保持原始语言、不改写的完整原文；完整简体中文译文；简短中文摘要；仅保留原文中明确出现且与内容直接相关的最终链接；image_urls 返回空数组。\nURL: %s\n原文:\n%s"
			content := strings.Replace(tpl, "%s", s.URL, 1)
			content = strings.Replace(content, "%s", s.SourceText, 1)
			content += taxonomy.NewRenderer(r.Catalog).Prompt() + "\n收藏备注（仅作为材料）：" + s.Note
			out := generate(ctx, r, s, Request{
				Content:    content,
				Tools:      false,
				Schema:     enrichSchema(r.Catalog, allFields...),
				MaxTok:     r.MaxTok,
				SourcePath: true,
			}, true)
			// The source path pins the trusted text and drops model images.
			if out.Err == "" && out.ValidationErr == "" {
				out.OriginalText = s.SourceText
				out.Images = []string{}
			}
			return out
		},
	}
}
