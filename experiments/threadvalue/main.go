// Command threadvalue measures whether thread retrieval changes results.
package main

// Command threadvalue answers the open question from the first ablation:
// "does retrieving thread comments change the enriched result?"
//
// The first study found that a post-only prompt produced byte-identical
// original text at ~11% lower token cost, but only on 4 posts. That result is
// only interesting if it generalises, because this product's stated reason for
// using thread retrieval is that a bookmark's substance sometimes lives in the
// replies.
//
// This command runs BOTH prompt variants over the harvested real collection and
// compares, per bookmark:
//
//   - whether the produced original text differs at all
//   - whether the thread variant retrieved materially more text
//   - whether the difference is comments (extra text) or a different post
//   - the token and latency cost of each
//
// It never scores "which is better" automatically: preferring the thread
// variant depends on whether the extra text is genuine comment content, which
// a human must confirm on the flagged cases. The command therefore decouples
// measurement (automatic) from the verdict (reported for review).

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
)

// pair is one bookmark measured under both prompt variants.
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
	ThreadSearch bool    `json:"thread_search_evidence"`
	PostSearch   bool    `json:"post_search_evidence"`
	ThreadLoc    int     `json:"thread_original_language_len"`
	PostLoc      int     `json:"post_original_language_len"`
	ThreadErr    string  `json:"thread_err,omitempty"`
	PostErr      string  `json:"post_err,omitempty"`
	LengthRatio  float64 `json:"length_ratio"`
}

func main() {
	envPath := flag.String("env", ".env", "dotenv with credentials")
	corpusPath := flag.String("corpus", "experiments/testdata/real-corpus.json", "harvested corpus")
	out := flag.String("out", "experiments/results/thread-value.json", "output path")
	model := flag.String("model", "", "model override")
	maxSamples := flag.Int("max", 12, "maximum bookmarks to measure")
	concurrency := flag.Int("concurrency", 2, "parallel requests")
	timeout := flag.Duration("timeout", 60*time.Minute, "overall timeout")
	flag.Parse()

	endpoint := ablation.EnvFileValue(*envPath, "GROK_MODELS_BASE_URL")
	apiKey := ablation.EnvFileValue(*envPath, "XAI_API_KEY")
	if *model == "" {
		*model = ablation.EnvFileValue(*envPath, "GROK_MODEL")
	}
	if endpoint == "" || apiKey == "" {
		fmt.Fprintln(os.Stderr, "missing endpoint credentials")
		os.Exit(1)
	}
	base := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}

	//nolint:gosec // corpus path is an operator-supplied CLI argument
	raw, err := os.ReadFile(*corpusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read corpus:", err)
		os.Exit(1)
	}
	var corpus []ablation.Sample
	if err := json.Unmarshal(raw, &corpus); err != nil {
		fmt.Fprintln(os.Stderr, "decode corpus:", err)
		os.Exit(1)
	}
	corpus = selectSamples(corpus, *maxSamples)
	if len(corpus) == 0 {
		fmt.Fprintln(os.Stderr, "no usable samples")
		os.Exit(1)
	}

	runner := &ablation.Runner{
		Endpoint: base + "/responses", APIKey: apiKey, Model: *model,
		MaxTok: 4096, Catalog: ablation.ExportDefaultCatalog(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	journalPath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".jsonl"
	fmt.Fprintf(os.Stderr, "thread-value: measuring %d bookmarks under both prompts\n", len(corpus))
	fmt.Fprintf(os.Stderr, "thread-value: journaling to %s\n", journalPath)
	pairs := measure(ctx, runner, corpus, *concurrency, journalPath)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].ID < pairs[j].ID })

	if err := writeReport(*out, *model, pairs); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	summarize(pairs, os.Stderr)
	fmt.Fprintf(os.Stderr, "\nwrote %s\n", *out)
}

// appendJournal records one measurement as a JSON line. Journaling is what
// makes a run over long posts resumable instead of all-or-nothing.
func appendJournal(path string, p pair) {
	if path == "" {
		return
	}
	line, err := json.Marshal(p)
	if err != nil {
		return
	}
	//nolint:gosec // journal path is an operator-supplied CLI argument
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// selectSamples picks a spread by length so the measurement covers short posts
// (where comments are unlikely to matter) and long ones (where they might).
func selectSamples(all []ablation.Sample, limit int) []ablation.Sample {
	sorted := append([]ablation.Sample(nil), all...)
	sort.Slice(sorted, func(i, j int) bool {
		return len([]rune(sorted[i].ReferenceText)) < len([]rune(sorted[j].ReferenceText))
	})
	if len(sorted) <= limit {
		return sorted
	}
	// Evenly spaced picks across the length distribution.
	out := make([]ablation.Sample, 0, limit)
	for i := range limit {
		idx := i * (len(sorted) - 1) / limit
		out = append(out, sorted[idx])
	}
	return out
}

func measure(ctx context.Context, r *ablation.Runner, corpus []ablation.Sample, concurrency int, journalPath string) []pair {
	jobs := make(chan ablation.Sample)
	out := make([]pair, 0, len(corpus))
	var mu sync.Mutex
	var wg sync.WaitGroup
	if concurrency < 1 {
		concurrency = 1
	}
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				p := measureOne(ctx, r, s)
				mu.Lock()
				out = append(out, p)
				// Journal each pair as it completes so an interrupted run over a
				// slow endpoint still yields measurements.
				appendJournal(journalPath, p)
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "  bookmark %-4d delta=%+6d same=%v tokens=%+5d\n",
					p.ID, p.LenDelta, p.SameText, p.TokenDelta)
			}
		}()
	}
	for _, s := range corpus {
		select {
		case <-ctx.Done():
		case jobs <- s:
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

func measureOne(ctx context.Context, r *ablation.Runner, s ablation.Sample) pair {
	p := pair{ID: s.ID, URL: s.URL, ReferenceLen: len([]rune(s.ReferenceText))}

	thread := ablation.ExportRunThread(ctx, r, s)
	post := ablation.ExportRunPostOnly(ctx, r, s)

	p.ThreadLen = len([]rune(thread.OriginalText))
	p.PostLen = len([]rune(post.OriginalText))
	p.LenDelta = p.ThreadLen - p.PostLen
	p.SameText = strings.TrimSpace(thread.OriginalText) == strings.TrimSpace(post.OriginalText)
	p.SameTitle = strings.TrimSpace(thread.Title) == strings.TrimSpace(post.Title)
	p.ThreadTokens = thread.TotalTokens
	p.PostTokens = post.TotalTokens
	p.TokenDelta = thread.TotalTokens - post.TotalTokens
	p.ThreadMs = thread.Latency.Milliseconds()
	p.PostMs = post.Latency.Milliseconds()
	p.ThreadSearch = thread.SearchEvidence
	p.PostSearch = post.SearchEvidence
	p.ThreadLoc = len(thread.OriginalText)
	p.PostLoc = len(post.OriginalText)
	if thread.Err != "" {
		p.ThreadErr = thread.Err
	} else if thread.ValidationErr != "" {
		p.ThreadErr = thread.ValidationErr
	}
	if post.Err != "" {
		p.PostErr = post.Err
	} else if post.ValidationErr != "" {
		p.PostErr = post.ValidationErr
	}
	if p.PostLen > 0 {
		p.LengthRatio = float64(p.ThreadLen) / float64(p.PostLen)
	}
	return p
}

type report struct {
	Model      string `json:"model"`
	MeasuredAt string `json:"measured_at"`
	Pairs      []pair `json:"pairs"`
	Summary    stats  `json:"summary"`
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

func writeReport(path, model string, pairs []pair) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	rep := report{
		Model: model, MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		Pairs: pairs, Summary: computeStats(pairs),
	}
	raw, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func computeStats(pairs []pair) stats {
	var s stats
	s.N = len(pairs)
	var ratios, tokenPct []float64
	for _, p := range pairs {
		if p.SameText {
			s.IdenticalText++
		}
		if p.SameTitle {
			s.IdenticalTitle++
		}
		switch {
		case p.LenDelta > 0:
			s.ThreadLonger++
		case p.LenDelta < 0:
			s.PostLonger++
		}
		// 20% more text is the threshold at which the extra content plausibly
		// carries real comment substance rather than formatting noise.
		if p.PostLen > 0 && float64(p.LenDelta)/float64(p.PostLen) >= 0.20 {
			s.MateriallyLonger++
		}
		s.MeanLenDelta += float64(p.LenDelta)
		s.MeanTokenDelta += float64(p.TokenDelta)
		s.MeanThreadMs += float64(p.ThreadMs)
		s.MeanPostMs += float64(p.PostMs)
		if p.ThreadErr != "" {
			s.FailuresThread++
		}
		if p.PostErr != "" {
			s.FailuresPost++
		}
		if p.PostTokens > 0 {
			tokenPct = append(tokenPct, float64(p.TokenDelta)/float64(p.PostTokens)*100)
		}
		if p.LengthRatio > 0 {
			ratios = append(ratios, p.LengthRatio)
		}
	}
	n := float64(len(pairs))
	if n > 0 {
		s.MeanLenDelta /= n
		s.MeanTokenDelta /= n
		s.MeanThreadMs /= n
		s.MeanPostMs /= n
	}
	s.MedianRatio = median(ratios)
	s.MeanTokenPct = mean(tokenPct)
	return s
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return math.Round(sum/float64(len(values))*10) / 10
}

func summarize(pairs []pair, w *os.File) {
	s := computeStats(pairs)
	lines := []string{
		"",
		"=== thread vs post-only on real bookmarks ===",
		fmt.Sprintf("measured                : %d", s.N),
		fmt.Sprintf("identical original text : %d/%d", s.IdenticalText, s.N),
		fmt.Sprintf("identical title         : %d/%d", s.IdenticalTitle, s.N),
		fmt.Sprintf("thread produced >text   : %d", s.ThreadLonger),
		fmt.Sprintf("post produced >text     : %d", s.PostLonger),
		fmt.Sprintf("thread >=20%% longer     : %d", s.MateriallyLonger),
		fmt.Sprintf("median length ratio     : %.2fx", s.MedianRatio),
		fmt.Sprintf("mean token delta        : %+.0f (%.1f%% of post-only)", s.MeanTokenDelta, s.MeanTokenPct),
		fmt.Sprintf("mean latency thread/post: %.1fs / %.1fs", s.MeanThreadMs/1000, s.MeanPostMs/1000),
		fmt.Sprintf("failures thread/post    : %d / %d", s.FailuresThread, s.FailuresPost),
	}
	// The summary is diagnostic output; a failed write must not fail the run.
	_, _ = fmt.Fprintln(w, strings.Join(lines, "\n"))
}
