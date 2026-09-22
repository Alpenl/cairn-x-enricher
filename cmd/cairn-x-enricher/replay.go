package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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
	var commit, authorizeWrite bool
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
			run, err := selectReplayRun(runs)
			if err != nil {
				return err
			}
			// The exact question definition is loaded from the Worker. Deriving
			// the dimension or the level order from the question ID would guess
			// at meanings the run never recorded (F06).
			storedSpec, err := queue.GetQuestionSpec(ctx, run.SpecID)
			if err != nil {
				return fmt.Errorf("load stored question spec: %w", err)
			}
			spec, err := classify.DecodeSpec(storedSpec.Payload)
			if err != nil {
				return fmt.Errorf("stored question spec is not decodable: %w", err)
			}
			if spec.SpecID != run.SpecID || (run.SpecHash != "" && spec.SemanticHash != run.SpecHash) {
				return fmt.Errorf("stored question spec %s does not match the run's recorded identity", run.SpecID)
			}
			raw, err := run.DecodeJudgments(spec)
			if err != nil {
				return fmt.Errorf("stored run cannot be replayed: %w", err)
			}
			// The historical policy payload is required. Substituting a renamed
			// default would fabricate a baseline that was never used.
			oldPolicy, err := classify.DecodePolicy(run.Policy)
			if err != nil {
				return fmt.Errorf("stored run cannot be replayed under its historical policy: %w", err)
			}
			newPolicy := oldPolicy
			newPolicy.Version = oldPolicy.Version + "+replay"
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
				"historical_policy": oldPolicy.Version, "new_policy": newPolicy.Version,
				"model_calls": 0, "changed": changed, "before": before, "after": after,
			}
			if commit {
				if !authorizeWrite {
					// Dry-run is the default. A write requires the explicit
					// second opt-in; refusing here is the safety feature, not a
					// missing implementation.
					output["committed"] = false
					output["committed_reason"] = "write requires --authorize-write (dry-run is the default)"
				} else {
					if err := queue.SubmitDecision(ctx, id, map[string]any{
						"operation_key":    fmt.Sprintf("replay-%d-%d-%s", id, run.ID, newPolicy.Version),
						"run_ids":          []int64{run.ID},
						"policy_version":   newPolicy.Version,
						"policy":           newPolicy,
						"spec_id":          run.SpecID,
						"requested_model":  run.RequestedModel,
						"content_revision": run.ContentRevision,
						"automatic":        classify.AutomaticFromProposals(after),
					}); err != nil {
						return fmt.Errorf("commit decision: %w", err)
					}
					output["committed"] = true
				}
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
	command.Flags().BoolVar(&commit, "commit", false, "request a controlled commit of the new decision (dry-run by default)")
	command.Flags().BoolVar(&authorizeWrite, "authorize-write", false, "explicitly authorize the controlled decision write")
	return command
}

// selectReplayRun picks the newest succeeded, complete run. A trailing
// partial/failed run is reported rather than silently replayed as if it were
// the current decision (F06).
func selectReplayRun(runs []cairn.StoredRun) (cairn.StoredRun, error) {
	if len(runs) == 0 {
		return cairn.StoredRun{}, fmt.Errorf("bookmark has no stored run to replay")
	}
	for index := len(runs) - 1; index >= 0; index-- {
		run := runs[index]
		if run.Status != "succeeded" || run.Coverage != "complete" {
			continue
		}
		if len(run.Answers) == 0 {
			continue
		}
		return run, nil
	}
	last := runs[len(runs)-1]
	return cairn.StoredRun{}, fmt.Errorf("the newest stored run (id %d, status %s, coverage %s) is not a complete success; there is no earlier complete run to replay",
		last.ID, last.Status, last.Coverage)
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
			payload, err := queue.RefreshSource(context.Background(), id)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(payload))
			return err
		},
	}
	command.Flags().Int64Var(&id, "id", 0, "bookmark id to re-enrol for source retrieval")
	return command
}
