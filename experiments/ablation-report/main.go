// Command ablation-report renders the persisted experiment artifacts as a
// single Markdown document, including the identity-fidelity probe.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ---------- ablation report ----------

type Score struct {
	Accepted         bool    `json:"accepted"`
	InfraFailed      bool    `json:"infra_failed"`
	RejectReason     string  `json:"reject_reason"`
	HasTranslation   bool    `json:"has_translation"`
	OriginalFidelity float64 `json:"original_fidelity"`
	LanguageCorrect  bool    `json:"language_correct"`
	LinksValid       bool    `json:"links_valid"`
	Grounded         bool    `json:"grounded"`
	ImagesValid      bool    `json:"images_valid"`
	ImageCount       int     `json:"image_count"`
	TopicsSelected   int     `json:"topics_selected"`
	SearchEvidence   bool    `json:"search_evidence"`
	FabricationRisk  bool    `json:"fabrication_risk"`
	Tokens           int     `json:"tokens"`
	LatencyMs        int64   `json:"latency_ms"`
	FieldYield       float64 `json:"field_yield"`
	QualityScore     float64 `json:"quality_score"`
}

type Outcome struct {
	Title          string   `json:"Title"`
	Summary        string   `json:"Summary"`
	Language       string   `json:"Language"`
	OriginalText   string   `json:"OriginalText"`
	TranslatedText string   `json:"TranslatedText"`
	Links          []string `json:"Links"`
	Images         []string `json:"Images"`
	SearchEvidence bool     `json:"SearchEvidence"`
	Err            string   `json:"Err"`
	ValidationErr  string   `json:"ValidationErr"`
	InfraFailed    bool     `json:"InfraFailed"`
}

type Record struct {
	Variant string  `json:"variant"`
	Ablates string  `json:"ablates"`
	Sample  int64   `json:"sample"`
	Outcome Outcome `json:"outcome"`
	Score   Score   `json:"score"`
	Skipped string  `json:"skipped"`
}

type Summary struct {
	Variant            string  `json:"variant"`
	Ablates            string  `json:"ablates"`
	Runs               int     `json:"runs"`
	Accepted           int     `json:"accepted"`
	AcceptRate         float64 `json:"accept_rate"`
	MeanQuality        float64 `json:"mean_quality"`
	MeanFidelity       float64 `json:"mean_fidelity"`
	MeanFieldYield     float64 `json:"mean_field_yield"`
	MeanTokens         float64 `json:"mean_tokens"`
	MeanLatencyMs      float64 `json:"mean_latency_ms"`
	SearchEvidence     int     `json:"search_evidence"`
	FabricationRisk    int     `json:"fabrication_risk"`
	LanguageErrors     int     `json:"language_errors"`
	TranslationMiss    int     `json:"translation_missing"`
	ClassificationMiss int     `json:"classification_missing"`
	Failures           int     `json:"failures"`
	Skipped            int     `json:"skipped"`
	InfraFailed        int     `json:"infra_failed"`
	QualityDelta       float64 `json:"quality_delta_vs_full"`
	TokenDelta         float64 `json:"token_delta_vs_full"`
	LatencyDelta       float64 `json:"latency_delta_vs_full"`
}

type Report struct {
	Model      string    `json:"model"`
	Endpoint   string    `json:"endpoint"`
	Samples    int       `json:"samples"`
	Variants   int       `json:"variants"`
	ModelCalls int       `json:"model_calls"`
	DurationMS int64     `json:"duration_ms"`
	Summary    []Summary `json:"summary"`
	Records    []Record  `json:"records"`
}

// ---------- identity probe report ----------

type IdentityResult struct {
	Case           string `json:"case"`
	URL            string `json:"url"`
	ClaimedAuthor  string `json:"claimed_author"`
	ResolvedAuthor string `json:"resolved_author"`
	ResolvedLink   string `json:"resolved_link"`
	Status         string `json:"status"`
}

type IdentityReport struct {
	Model      string           `json:"model"`
	Method     string           `json:"method"`
	Conclusion string           `json:"conclusion"`
	Results    []IdentityResult `json:"results"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ablation-report <dir>")
		os.Exit(2)
	}
	dir := strings.TrimRight(os.Args[1], "/")
	var b strings.Builder

	fmt.Fprintf(&b, "# 消融实验最终报告 — Cairn X Enricher\n\n")
	fmt.Fprintf(&b, "本报告由 `experiments/` 下的可复现实验程序生成，全部数据来自对真实模型端点的实际调用。\n\n")

	identityPath := dir + "/identity.json"
	//nolint:gosec // paths are the CLI argument, not network input
	if raw, err := os.ReadFile(identityPath); err == nil {
		var ir IdentityReport
		if json.Unmarshal(raw, &ir) == nil {
			renderIdentity(&b, ir)
		}
	}

	ablationPath := dir + "/ablation.json"
	//nolint:gosec // paths are the CLI argument, not network input
	if raw, err := os.ReadFile(ablationPath); err == nil {
		var r Report
		if json.Unmarshal(raw, &r) == nil {
			renderAblation(&b, r)
		}
	}
	fmt.Print(b.String())
}

func renderIdentity(b *strings.Builder, ir IdentityReport) {
	fmt.Fprintf(b, "## 一、输入语料的 URL 身份校验\n\n")
	fmt.Fprintf(b, "### 为什么必须先做这一步\n\n")
	fmt.Fprintf(b, "消融实验的第一版语料把 post ID `1991910395720925418` 标成 `xai` 的帖子，\n")
	fmt.Fprintf(b, "但该 ID 实际属于 `@karpathy`（Animals vs Ghosts）。模型忠实返回了 Karpathy 的正文，\n")
	fmt.Fprintf(b, "却因为 URL 标签错误而被判为“身份错误”。这说明：**未校验的语料会伪造出并不存在的缺陷**。\n")
	fmt.Fprintf(b, "因此每个 sample URL 在计分前都必须经过下面的解析校验。\n\n")
	fmt.Fprintf(b, "### 方法\n\n%s\n\n", ir.Method)
	fmt.Fprintf(b, "| case | URL 声称作者 | 工具解析作者 | 解析链接 | 结论 |\n")
	fmt.Fprintf(b, "| --- | --- | --- | --- | --- |\n")
	allOK := true
	for _, r := range ir.Results {
		mark := r.Status
		if r.Status != "OK" {
			allOK = false
			mark = "**" + r.Status + "**"
		}
		fmt.Fprintf(b, "| %s | @%s | @%s | %s | %s |\n",
			esc(r.Case), esc(r.ClaimedAuthor), esc(r.ResolvedAuthor), esc(r.ResolvedLink), mark)
	}
	fmt.Fprintf(b, "\n")
	if allOK {
		fmt.Fprintf(b, "**结论：全部 sample URL 均解析到声称的作者，语料可信。**\n\n")
	}
	fmt.Fprintf(b, "%s\n\n", ir.Conclusion)
}

func renderAblation(b *strings.Builder, r Report) {
	fmt.Fprintf(b, "## 二、消融实验矩阵\n\n")
	fmt.Fprintf(b, "| 字段 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(b, "| model | `%s` |\n", r.Model)
	fmt.Fprintf(b, "| endpoint | `%s` |\n", r.Endpoint)
	fmt.Fprintf(b, "| variants | %d |\n", r.Variants)
	fmt.Fprintf(b, "| samples/variant | %d |\n", r.Samples)
	fmt.Fprintf(b, "| 模型调用总数 | %d |\n", r.ModelCalls)
	fmt.Fprintf(b, "| 墙钟耗时 | %s |\n\n", ms(r.DurationMS))

	sums := append([]Summary(nil), r.Summary...)
	sort.SliceStable(sums, func(i, j int) bool {
		if (sums[i].Variant == "FULL") != (sums[j].Variant == "FULL") {
			return sums[i].Variant == "FULL"
		}
		return sums[i].MeanQuality > sums[j].MeanQuality
	})
	fmt.Fprintf(b, "### 汇总\n\n")
	fmt.Fprintf(b, "| 变体 | 质量 | Δ质量 | 通过率 | 原文保真 | 字段产出 | tokens | Δtokens | 延迟 | 搜索证据 | 语言错 | 结构失败 | 上游失败 | 跳过 |\n")
	fmt.Fprintf(b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range sums {
		fmt.Fprintf(b, "| `%s` | %.3f | %s | %.0f%% | %.2f | %.2f | %.0f | %s | %.1fs | %d/%d | %d | %d | %d | %d |\n",
			s.Variant, s.MeanQuality, signed(s.QualityDelta, 3), s.AcceptRate*100,
			s.MeanFidelity, s.MeanFieldYield, s.MeanTokens, signed(s.TokenDelta, 0),
			s.MeanLatencyMs/1000, s.SearchEvidence, s.Runs, s.LanguageErrors,
			s.Failures, s.InfraFailed, s.Skipped)
	}
	fmt.Fprintf(b, "\n> 上游失败与跳过不计入质量均值，避免端点抖动被误读为质量回退。\n\n")

	fmt.Fprintf(b, "### 各变体消融的设计决策\n\n")
	fmt.Fprintf(b, "| 变体 | 被消融的决策 |\n| --- | --- |\n")
	for _, s := range sums {
		fmt.Fprintf(b, "| `%s` | %s |\n", s.Variant, s.Ablates)
	}
	fmt.Fprintf(b, "\n")

	fmt.Fprintf(b, "### 逐样本明细\n\n")
	for _, v := range orderedVariants(r.Records) {
		fmt.Fprintf(b, "#### `%s`\n\n", v)
		fmt.Fprintf(b, "| sample | 通过 | 质量 | 标题 | 语言 | 原文 | 译文 | 链接 | 图片 | tokens | 延迟 | 备注 |\n")
		fmt.Fprintf(b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, rec := range r.Records {
			if rec.Variant != v {
				continue
			}
			note := rec.Score.RejectReason
			if rec.Skipped != "" {
				note = "SKIPPED: " + rec.Skipped
			} else if rec.Score.InfraFailed {
				note = "上游失败（不计分）"
			}
			fmt.Fprintf(b, "| %d | %s | %.2f | %s | %s | %d | %d | %d | %d | %d | %.1fs | %s |\n",
				rec.Sample, yesno(rec.Score.Accepted), rec.Score.QualityScore,
				esc(trim(rec.Outcome.Title, 30)), esc(rec.Outcome.Language),
				len([]rune(rec.Outcome.OriginalText)), len([]rune(rec.Outcome.TranslatedText)),
				len(rec.Outcome.Links), len(rec.Outcome.Images),
				rec.Score.Tokens, float64(rec.Score.LatencyMs)/1000, esc(trim(note, 55)))
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### 失败模式统计\n\n")
	seen := map[string]int{}
	for _, rec := range r.Records {
		reason := rec.Score.RejectReason
		if reason == "" {
			continue
		}
		seen[rec.Variant+" :: "+trim(reason, 80)]++
	}
	if len(seen) == 0 {
		fmt.Fprintf(b, "无拒绝记录。\n")
	} else {
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, "- %s (×%d)\n", esc(k), seen[k])
		}
	}
}

func orderedVariants(records []Record) []string {
	seen := map[string]bool{}
	var out []string
	for _, rec := range records {
		if !seen[rec.Variant] {
			seen[rec.Variant] = true
			out = append(out, rec.Variant)
		}
	}
	sort.Strings(out)
	return out
}

func ms(v int64) string {
	if v < 1000 {
		return fmt.Sprintf("%dms", v)
	}
	return fmt.Sprintf("%.1fs", float64(v)/1000)
}

func signed(v float64, digits int) string {
	if v == 0 {
		return "—"
	}
	sign := "+"
	if v < 0 {
		sign = ""
	}
	return fmt.Sprintf("%s%.*f", sign, digits, v)
}

func yesno(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
