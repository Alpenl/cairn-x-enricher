// Command threadreport renders the thread-value measurement as Markdown.
package main

// Command threadreport renders the thread-value measurement as Markdown.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type pair struct {
	ID           int64   `json:"id"`
	URL          string  `json:"url"`
	ReferenceLen int     `json:"reference_len"`
	ThreadLen    int     `json:"thread_len"`
	PostLen      int     `json:"post_len"`
	LenDelta     int     `json:"len_delta"`
	SameText     bool    `json:"same_text"`
	SameTitle    bool    `json:"same_title"`
	ThreadTokens int     `json:"thread_tokens"`
	PostTokens   int     `json:"post_tokens"`
	TokenDelta   int     `json:"token_delta"`
	ThreadMs     int64   `json:"thread_ms"`
	PostMs       int64   `json:"post_ms"`
	LengthRatio  float64 `json:"length_ratio"`
	ThreadErr    string  `json:"thread_err"`
	PostErr      string  `json:"post_err"`
}

type stats struct {
	N                int     `json:"n"`
	IdenticalText    int     `json:"identical_text"`
	IdenticalTitle   int     `json:"identical_title"`
	ThreadLonger     int     `json:"thread_longer"`
	PostLonger       int     `json:"post_longer"`
	MeanLenDelta     float64 `json:"mean_len_delta"`
	MedianRatio      float64 `json:"median_length_ratio"`
	MeanTokenDelta   float64 `json:"mean_token_delta"`
	MeanTokenPct     float64 `json:"mean_token_pct"`
	MeanThreadMs     float64 `json:"mean_thread_ms"`
	MeanPostMs       float64 `json:"mean_post_ms"`
	FailuresThread   int     `json:"failures_thread"`
	FailuresPost     int     `json:"failures_post"`
	MateriallyLonger int     `json:"materially_longer"`
}

type report struct {
	Model      string `json:"model"`
	MeasuredAt string `json:"measured_at"`
	Pairs      []pair `json:"pairs"`
	Summary    stats  `json:"summary"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: threadreport <thread-value.json>")
		os.Exit(2)
	}
	//nolint:gosec // path is the CLI argument
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Print(render(r))
}

func render(r report) string {
	var b strings.Builder
	s := r.Summary

	fmt.Fprintf(&b, "## 三、真实收藏上的线程读取复测\n\n")
	fmt.Fprintf(&b, "第一轮消融只有 4 条语料，得出“线程读取输出逐字节相同却多花 ~11%% token”的结论。\n")
	fmt.Fprintf(&b, "本节用**真实收藏库**（从 Worker 拉取 %d 条已完成、含原文的 X 书签，按长度均匀取样）复测该结论。\n\n", s.N)
	fmt.Fprintf(&b, "模型 `%s`，每个书签分别用“读线程”与“只读原帖”两种提示词各跑一次。\n\n", r.Model)

	fmt.Fprintf(&b, "### 汇总\n\n")
	fmt.Fprintf(&b, "| 指标 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| 测量书签数 | %d |\n", s.N)
	fmt.Fprintf(&b, "| 原文**逐字节相同** | %d/%d (%.0f%%) |\n", s.IdenticalText, s.N, pct(s.IdenticalText, s.N))
	fmt.Fprintf(&b, "| 标题相同 | %d/%d（**注意**：标题本身非确定性，见下方对照实验） |\n", s.IdenticalTitle, s.N)
	fmt.Fprintf(&b, "| 线程变体原文更长 | %d |\n", s.ThreadLonger)
	fmt.Fprintf(&b, "| post-only 原文更长 | %d |\n", s.PostLonger)
	fmt.Fprintf(&b, "| 线程**实质更长**（≥20%%） | %d |\n", s.MateriallyLonger)
	fmt.Fprintf(&b, "| 长度中位比（线程/原帖） | %.2f× |\n", s.MedianRatio)
	fmt.Fprintf(&b, "| 平均 token 差 | %+.0f (%.1f%%) |\n", s.MeanTokenDelta, s.MeanTokenPct)
	fmt.Fprintf(&b, "| 平均延迟 线程/post-only | %.1fs / %.1fs |\n", s.MeanThreadMs/1000, s.MeanPostMs/1000)
	fmt.Fprintf(&b, "| 失败 线程/post-only | %d / %d |\n\n", s.FailuresThread, s.FailuresPost)

	fmt.Fprintf(&b, "### 对照实验：标题是否稳定？\n\n")
	fmt.Fprintf(&b, "上表显示标题几乎总是不相同。为判断这是“评论改变了标题”还是“标题本身随机”，\n")
	fmt.Fprintf(&b, "对**同一条书签用完全相同的线程提示词连跑 3 次**：\n\n")
	fmt.Fprintf(&b, "| 运行 | 原文长度 | 标题 |\n| --- | --- | --- |\n")
	fmt.Fprintf(&b, "| 1 | 97 | 福滤娃插件仓库突破700星作者致谢 |\n")
	fmt.Fprintf(&b, "| 2 | 97 | 福滤娃插件仓库获700+星标作者致谢 |\n")
	fmt.Fprintf(&b, "| 3 | 97 | 福滤娃插件仓库获超700星标作者致谢 |\n\n")
	fmt.Fprintf(&b, "原文三次完全一致（97 字符），标题三次全不相同。**因此“标题不同”是采样随机性，\n")
	fmt.Fprintf(&b, "不是评论内容带来的信息差异**；不能用标题差异证明线程读取有价值。\n\n")

	fmt.Fprintf(&b, "### 逐书签明细\n\n")
	fmt.Fprintf(&b, "| 书签 | 已存原文 | 线程原文 | 原帖原文 | Δ | 相同 | 线程token | 原帖token | Δtoken | 说明 |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	pairs := append([]pair(nil), r.Pairs...)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].ID < pairs[j].ID })
	for _, p := range pairs {
		note := ""
		switch {
		case p.ThreadErr != "":
			note = "线程失败: " + trim(p.ThreadErr, 30)
		case p.PostErr != "":
			note = "原帖失败: " + trim(p.PostErr, 30)
		case p.SameText:
			note = "完全相同"
		case p.LenDelta > 0:
			note = fmt.Sprintf("线程多 %.0f%%", ratioPct(p.LenDelta, p.PostLen))
		case p.LenDelta < 0:
			note = "原帖更长"
		}
		fmt.Fprintf(&b, "| %d | %d | %d | %d | %+d | %s | %d | %d | %+d | %s |\n",
			p.ID, p.ReferenceLen, p.ThreadLen, p.PostLen, p.LenDelta,
			yesno(p.SameText), p.ThreadTokens, p.PostTokens, p.TokenDelta, esc(note))
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func ratioPct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func yesno(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func esc(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
