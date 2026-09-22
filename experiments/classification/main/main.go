// Command classification-eval scores a labelled dataset, searches thresholds on
// the training split, applies the promotion gate and prints a machine-readable
// report. All of it is offline: only the explicit `-live` flag may call a model,
// and it is never invoked by make verify or CI.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func main() {
	var datasetPath string
	var gatePath string
	var dryRun bool
	var live bool
	var maxCalls int
	var liveConfig liveConfig
	flag.StringVar(&datasetPath, "dataset", "", "path to a dataset JSON file (samples + predictions)")
	flag.StringVar(&gatePath, "gate", "", "optional path to a gate threshold JSON file")
	flag.BoolVar(&dryRun, "dry-run", false, "print the plan without scoring or calling a model")
	flag.BoolVar(&live, "live", false, "explicitly opt in to a live model run (costs money)")
	flag.IntVar(&maxCalls, "max-calls", 0, "hard budget for a live run; 0 means no live run is permitted")
	flag.StringVar(&liveConfig.catalogPath, "catalog", "", "frozen taxonomy JSON for live/dry-run")
	flag.StringVar(&liveConfig.output, "output", "", "new PRIVATE artifact directory for a live run (never overwritten)")
	flag.StringVar(&liveConfig.envFile, "env-file", "", "explicit credential file; otherwise use TYPESAFE_API_KEY from environment")
	flag.StringVar(&liveConfig.model, "model", "jev-1.13.0", "pinned model with reviewed input ceiling")
	flag.StringVar(&liveConfig.baseURL, "base-url", "https://api.typesafe.ai", "TypeSafe API origin")
	flag.StringVar(&liveConfig.sampleID, "sample-id", "", "optional explicit single sample for a paid smoke test")
	flag.IntVar(&liveConfig.maxSamples, "max-samples", 0, "maximum samples in a live run")
	flag.Int64Var(&liveConfig.maxTokens, "max-tokens", 0, "hard INPUT-token reservation budget, 65536 reserved per attempt")
	flag.DurationVar(&liveConfig.timeout, "timeout", 60*time.Second, "per-call timeout")
	flag.Parse()

	if live && maxCalls <= 0 {
		fmt.Fprintln(os.Stderr, "error: a live run requires an explicit positive -max-calls budget")
		os.Exit(2)
	}

	if datasetPath == "" {
		fmt.Fprintln(os.Stderr, "error: -dataset is required")
		os.Exit(2)
	}

	// The path comes from an explicit operator flag, not from untrusted input.
	raw, err := os.ReadFile(filepath.Clean(datasetPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var dataset evaluation.Dataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		fmt.Fprintln(os.Stderr, "error: decode dataset:", err)
		os.Exit(1)
	}
	if err := dataset.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid dataset:", err)
		os.Exit(1)
	}

	gate := evaluation.DefaultGate()
	if gatePath != "" {
		gateRaw, err := os.ReadFile(filepath.Clean(gatePath))
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(gateRaw, &gate); err != nil {
			fmt.Fprintln(os.Stderr, "error: decode gate:", err)
			os.Exit(1)
		}
	}

	if live {
		liveConfig.maxCalls = maxCalls
		liveConfig.dryRun = dryRun
		if err := runLiveCommand(dataset, liveConfig, gate); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	if dryRun {
		plan := map[string]any{
			"dataset": dataset.Name, "split": dataset.Split,
			"samples": len(dataset.Samples), "predictions": len(dataset.Prediction),
			"gate": gate, "model_calls": 0,
			"note": "dry-run scores nothing and calls no model",
		}
		encode(plan)
		return
	}

	report, err := evaluation.Score(dataset)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: score:", err)
		os.Exit(1)
	}
	decision := evaluation.EvaluateGate(report, gate)
	encode(map[string]any{"report": report, "gate": gate, "decision": decision})
}

func encode(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: encode:", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
