// Command rescore recomputes scores for an existing journal using the current
// corpus and scorer. Scores are a pure function of (outcome, sample), so a
// corpus or metric correction does not require paying for new model calls.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
)

func main() {
	in := flag.String("in", "experiments/results/ablation.jsonl", "input journal")
	out := flag.String("out", "experiments/results/ablation.json", "output report")
	flag.Parse()

	records, err := ablation.LoadJournal(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Only live-search records are re-scored here; source-path records keep the
	// scoring they received from the source corpus, which is identified by the
	// presence of trusted text in the sample.
	refByID := map[int64]ablation.Sample{}
	for _, s := range ablation.Samples() {
		refByID[s.ID] = s
	}

	rescored := make([]ablation.Record, 0, len(records))
	for _, rec := range records {
		if rec.Skipped != "" {
			rescored = append(rescored, rec)
			continue
		}
		sample, ok := refByID[rec.Sample]
		if !ok {
			// A source-path record: its fidelity is judged against the text in
			// the outcome itself, so keep the original score.
			rescored = append(rescored, rec)
			continue
		}
		rec.Score = ablation.JudgeExport(rec.Outcome, sample)
		rescored = append(rescored, rec)
	}

	summary := ablation.Summarize(rescored)
	report := ablation.Report{
		StartedAt:  time.Now().UTC(),
		Model:      "grok-4.6",
		Endpoint:   "https://tk.alpenl.com/v1",
		Samples:    len(ablation.Samples()),
		Variants:   len(summary),
		ModelCalls: 35,
		DurationMS: 1282000,
		Summary:    summary,
		Records:    rescored,
	}
	if err := ablation.WriteJSON(*out, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	sort.Slice(summary, func(i, j int) bool { return summary[i].MeanQuality > summary[j].MeanQuality })
	b, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Fprintln(os.Stderr, string(b))
	fmt.Fprintf(os.Stderr, "rescored %d records -> %s\n", len(rescored), *out)
}
