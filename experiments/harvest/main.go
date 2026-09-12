// Command harvest pulls real enriched bookmarks from the Cairn Share Worker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// harvest pulls the real, already-enriched bookmarks from the Cairn Share
// Worker so experiments can run against production data instead of a
// hand-written corpus. This is what turns "does thread retrieval matter?"
// into a question answerable from the user's actual collection.
func main() {
	envPath := flag.String("env", ".env", "dotenv with Worker credentials")
	out := flag.String("out", "experiments/testdata/real-corpus.json", "output corpus path")
	limit := flag.Int("limit", 200, "maximum bookmarks to harvest")
	onlyWithText := flag.Bool("require-text", true, "keep only bookmarks with stored original text")
	flag.Parse()

	baseURL := ablation.EnvFileValue(*envPath, "CAIRN_API_BASE_URL")
	token := ablation.EnvFileValue(*envPath, "CAIRN_ENRICHER_TOKEN")
	if baseURL == "" || token == "" {
		fmt.Fprintln(os.Stderr, "missing CAIRN_API_BASE_URL or CAIRN_ENRICHER_TOKEN")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := cairn.NewClient(baseURL, token, nil)
	page, err := client.ListBookmarks(ctx, cairn.BookmarkQuery{Limit: *limit})
	if err != nil {
		fmt.Fprintln(os.Stderr, "list bookmarks:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "listed %d bookmarks (counts: %+v)\n", len(page.Items), page.Counts)

	var samples []ablation.Sample
	for _, item := range page.Items {
		if item.Source != "x" || !item.Processable {
			continue
		}
		detail, err := client.GetBookmark(ctx, item.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %d: %v\n", item.ID, err)
			continue
		}
		text := strings.TrimSpace(detail.OriginalText)
		if *onlyWithText && text == "" {
			continue
		}
		samples = append(samples, ablation.Sample{
			ID:            item.ID,
			URL:           detail.URL,
			Note:          detail.Note,
			ReferenceText: text,
		})
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i].ID < samples[j].ID })
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(samples, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, raw, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "harvested %d usable samples -> %s\n", len(samples), *out)
	reportStats(samples)
}

// reportStats prints the distribution that decides how much the corpus can
// support: short posts are cheap to reproduce, long ones expose truncation.
func reportStats(samples []ablation.Sample) {
	if len(samples) == 0 {
		return
	}
	var lens []int
	var withNote int
	for _, s := range samples {
		lens = append(lens, len([]rune(s.ReferenceText)))
		if strings.TrimSpace(s.Note) != "" {
			withNote++
		}
	}
	sort.Ints(lens)
	mean := 0
	for _, l := range lens {
		mean += l
	}
	mean /= len(lens)
	fmt.Fprintf(os.Stderr, "text length: min=%d p50=%d p90=%d max=%d mean=%d\n",
		lens[0], lens[len(lens)/2], lens[len(lens)*9/10], lens[len(lens)-1], mean)
	fmt.Fprintf(os.Stderr, "with note: %d/%d\n", withNote, len(samples))
}
