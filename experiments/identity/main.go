// Command identity probes whether the enrichment pipeline verifies that the
// text it writes back actually belongs to the requested URL.
//
// The production validator checks structure: title is Chinese and 8-32 runes,
// original_text is non-empty, links and images are well-formed. None of those
// rules can detect a response that is internally valid but describes a
// different post. This probe measures how often that happens and whether an
// existing production signal (the source URL echoed inside the retrieved text,
// or link/image provenance) can catch it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
)

func main() {
	envPath := flag.String("env", ".env", "dotenv with credentials")
	out := flag.String("out", "experiments/results/identity.json", "output path")
	model := flag.String("model", "", "model override")
	reps := flag.Int("reps", 3, "repetitions per case")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall timeout")
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

	// Each case is a URL plus the token that any faithful retrieval must
	// contain. `must` is a substring of the real post; `must_not` is a token
	// from a different, loudly-different post that must never appear.
	cases := []struct {
		name    string
		url     string
		must    []string
		mustNot []string
	}{
		{
			name:    "grok46_announcement",
			url:     "https://x.com/xai/status/1991910395720925418",
			must:    []string{"Grok 4.6"},
			mustNot: []string{"animal intelligence", "homeostasis", "shape shifter"},
		},
		{
			name:    "karpathy_intelligence_space",
			url:     "https://x.com/karpathy/status/1881276282861322459",
			must:    []string{},
			mustNot: []string{"Grok 4.6", "Introducing Grok"},
		},
	}

	type outcome struct {
		Case            string   `json:"case"`
		URL             string   `json:"url"`
		Rep             int      `json:"rep"`
		Title           string   `json:"title"`
		OriginalHead    string   `json:"original_head"`
		Language        string   `json:"language"`
		Tokens          int      `json:"tokens"`
		LatencyMS       int64    `json:"latency_ms"`
		SearchEvidence  bool     `json:"search_evidence"`
		ContainsMust    bool     `json:"contains_must"`
		ContainsMustNot bool     `json:"contains_must_not"`
		Err             string   `json:"err"`
		Links           []string `json:"links"`
		Images          []string `json:"images"`
	}

	results := []outcome{}
	client := &ablation.Runner{
		Endpoint: base + "/responses", APIKey: apiKey, Model: *model, MaxTok: 4096,
		Catalog: ablation.ExportDefaultCatalog(),
	}

	for _, c := range cases {
		for rep := 1; rep <= *reps; rep++ {
			o := outcome{Case: c.name, URL: c.url, Rep: rep}
			sample := ablation.Sample{ID: int64(len(results) + 1), URL: c.url, Note: "identity probe"}
			started := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			out := ablation.ExportRunFull(ctx, client, sample)
			cancel()
			o.LatencyMS = time.Since(started).Milliseconds()
			o.Tokens = out.TotalTokens
			o.SearchEvidence = out.SearchEvidence
			o.Title = out.Title
			o.Language = out.Language
			o.OriginalHead = ablation.ExportHead(out.OriginalText, 400)
			o.Links = out.Links
			o.Images = out.Images
			if out.Err != "" {
				o.Err = out.Err
			} else if out.ValidationErr != "" {
				o.Err = out.ValidationErr
			}
			hay := strings.ToLower(out.OriginalText + " " + out.TranslatedText + " " + out.Summary)
			o.ContainsMust = true
			for _, m := range c.must {
				if !strings.Contains(hay, strings.ToLower(m)) {
					o.ContainsMust = false
				}
			}
			for _, m := range c.mustNot {
				if strings.Contains(hay, strings.ToLower(m)) {
					o.ContainsMustNot = true
				}
			}
			results = append(results, o)
			fmt.Fprintf(os.Stderr, "%s rep%d: err=%q must=%v mustNot=%v title=%q\n",
				c.name, rep, o.Err, o.ContainsMust, o.ContainsMustNot, o.Title)
		}
	}

	if err := os.MkdirAll(dirOf(*out), 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(map[string]any{
		"model": *model, "endpoint": base, "results": results,
	}, "", "  ")
	if err := os.WriteFile(*out, raw, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}
