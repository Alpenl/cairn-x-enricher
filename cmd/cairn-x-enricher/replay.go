package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/spf13/cobra"
)

// newReplayCommand re-decides stored runs under a different policy. It never
// touches the network beyond fetching the stored run, and by default it does
// not write anything back: a replay is an inspection, not a mutation.
func newReplayCommand() *cobra.Command {
	var id int64
	var topicAccept, topicReject, choiceAccept float64
	var commit bool
	command := &cobra.Command{
		Use:   "replay",
		Short: "Re-decide a stored run under a new policy without calling the model",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id < 1 {
				return fmt.Errorf("--id must be a positive bookmark id")
			}
			cfg, err := config.LoadFor(config.RoleClassify)
			if err != nil {
				return err
			}
			httpClient := &http.Client{Timeout: cfg.RequestTimeout}
			queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, httpClient)
			ctx := context.Background()
			runs, err := queue.GetRuns(ctx, id)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				return fmt.Errorf("bookmark %d has no stored run to replay", id)
			}
			// The newest succeeded run is the one whose decision is current.
			run := runs[len(runs)-1]
			raw, err := classify.DecodeStoredJudgments(run.SpecID, run.SpecHash, run.RequestedModel, run.ResolvedModel, run.Answers, run.Coverage)
			if err != nil {
				return fmt.Errorf("stored run cannot be replayed: %w", err)
			}
			oldPolicy := classify.DefaultPolicy()
			// The stored policy version tells us which rule produced the run; the
			// exact thresholds are not persisted per run, so the baseline is used
			// as the comparison point and the change is reported explicitly.
			oldPolicy.Version = run.PolicyVersion
			newPolicy := classify.DefaultPolicy()
			newPolicy.Version = run.PolicyVersion + "+replay"
			if cmd.Flags().Changed("topic-accept") {
				newPolicy.TopicAccept = topicAccept
			}
			if cmd.Flags().Changed("topic-reject") {
				newPolicy.TopicReject = topicReject
			}
			if cmd.Flags().Changed("choice-accept") {
				newPolicy.ChoiceAccept = choiceAccept
			}
			before, after, changed, err := classify.Replay(raw, oldPolicy, newPolicy)
			if err != nil {
				return err
			}
			output := map[string]any{
				"link_id": id, "run_id": run.ID, "spec_id": run.SpecID,
				"model_calls": 0, "changed": changed, "before": before, "after": after,
			}
			if commit {
				// A controlled commit appends a new decision; it never creates a
				// model run and never deletes the old one.
				output["committed"] = false
				output["committed_reason"] = "controlled decision commit is not enabled without an explicit owner approval"
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
				return err
			}
			return nil
		},
	}
	command.Flags().Int64Var(&id, "id", 0, "bookmark id whose stored run should be replayed")
	command.Flags().Float64Var(&topicAccept, "topic-accept", 0.8, "topic accept threshold for the replayed policy")
	command.Flags().Float64Var(&topicReject, "topic-reject", 0.2, "topic reject threshold for the replayed policy")
	command.Flags().Float64Var(&choiceAccept, "choice-accept", 0.65, "choice accept threshold for the replayed policy")
	command.Flags().BoolVar(&commit, "commit", false, "request a controlled commit of the new decision (disabled by default)")
	return command
}

// newRefreshSourceCommand explicitly re-fetches a source. It is separate from
// replay so a zero-call replay can never accidentally trigger a paid retrieval.
func newRefreshSourceCommand() *cobra.Command {
	var id int64
	command := &cobra.Command{
		Use:   "refresh-source",
		Short: "Explicitly re-fetch a bookmark's source before reclassification",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id < 1 {
				return fmt.Errorf("--id must be a positive bookmark id")
			}
			cfg, err := config.LoadFor(config.RoleClassify)
			if err != nil {
				return err
			}
			httpClient := &http.Client{Timeout: cfg.RequestTimeout}
			queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, httpClient)
			if err := queue.RetryClassification(context.Background(), id); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "{\"id\":"+strconv.FormatInt(id, 10)+",\"status\":\"pending\"}")
			return err
		},
	}
	command.Flags().Int64Var(&id, "id", 0, "bookmark id to re-enrol for source retrieval")
	return command
}
