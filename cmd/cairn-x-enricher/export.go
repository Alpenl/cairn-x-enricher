package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/spf13/cobra"
)

// newExportDatasetCommand turns stored production runs into an offline dataset.
// It reads the Worker only: no model call, no gold fabrication, explicit link
// ids and a bounded count (B08-T08/T14 production ingestion).
func newExportDatasetCommand() *cobra.Command {
	var ids string
	var out string
	var name string
	var split string
	command := &cobra.Command{
		Use:   "export-dataset",
		Short: "Export stored classification runs as an offline evaluation dataset",
		RunE: func(cmd *cobra.Command, _ []string) error {
			linkIDs, err := parseLinkIDs(ids)
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) == "" {
				return fmt.Errorf("--out is required")
			}
			cfg, err := config.LoadFor(config.RoleClassify)
			if err != nil {
				return err
			}
			queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, &http.Client{Timeout: cfg.RequestTimeout})
			dataset, err := evaluation.ExportDataset(context.Background(), queue, evaluation.ExportOptions{
				LinkIDs: linkIDs, Name: name, Split: split,
			})
			if err != nil {
				return err
			}
			payload, err := evaluation.MarshalDataset(dataset)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, payload, 0o600); err != nil {
				return err
			}
			summary := map[string]any{
				"out": out, "name": dataset.Name, "split": dataset.Split,
				"samples": len(dataset.Samples), "predictions": len(dataset.Prediction),
				"gold": 0, "model_calls": 0,
				"note": "机器预测不是人工 gold；评分工具会将其报告为 inconclusive，直到人工标注。",
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		},
	}
	command.Flags().StringVar(&ids, "ids", "", "comma-separated bookmark ids (explicit and bounded)")
	command.Flags().StringVar(&out, "out", "", "path to write the dataset JSON")
	command.Flags().StringVar(&name, "name", "production-export", "dataset name")
	command.Flags().StringVar(&split, "split", "holdout", "dataset split label")
	return command
}

func parseLinkIDs(value string) ([]int64, error) {
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("invalid link id %q", trimmed)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("--ids must contain at least one positive bookmark id")
	}
	if len(ids) > 500 {
		return nil, fmt.Errorf("--ids is bounded to 500 bookmarks")
	}
	return ids, nil
}
