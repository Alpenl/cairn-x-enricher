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
			ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
			defer cancel()
			storedRun, err := queue.GetReplayableRun(ctx, id)
			if err != nil {
				return err
			}
			if storedRun == nil {
				return fmt.Errorf("bookmark has no complete succeeded run to replay")
			}
			run := *storedRun
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
			if cmd.Flags().Changed("topic-accept") {
				newPolicy.TopicAccept = topicAccept
			}
			if cmd.Flags().Changed("topic-reject") {
				newPolicy.TopicReject = topicReject
			}
			if cmd.Flags().Changed("choice-accept") {
				newPolicy.ChoiceAccept = choiceAccept
			}
			newPolicy, policyHash, err := classify.WithPolicyHashIdentity(newPolicy)
			if err != nil {
				return err
			}
			before, after, changed, err := classify.Replay(raw, oldPolicy, newPolicy)
			if err != nil {
				return err
			}
			output := map[string]any{
				"link_id": id, "run_id": run.ID, "spec_id": run.SpecID,
				"historical_policy": oldPolicy.Version, "new_policy": newPolicy.Version,
				"policy_hash": policyHash,
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
					view, err := queue.GetV2Selection(ctx, id)
					if err != nil {
						return fmt.Errorf("load replay revision: %w", err)
					}
					handshake, err := queue.Handshake(ctx, cairn.Capabilities{Protocol: "v2"})
					if err != nil {
						return fmt.Errorf("load replay target: %w", err)
					}
					runIDs := []int64{run.ID}
					operationKey, err := classify.PolicyReplayOperationKey(id, runIDs, policyHash, run.ContentRevision, run.SpecHash, run.RequestedModel, run.ResolvedModel, handshake.Target.Generation)
					if err != nil {
						return err
					}
					receipt, err := queue.SubmitPolicyReplay(ctx, id, cairn.PolicyReplayRequest{
						OperationKey: operationKey, RunIDs: runIDs, PolicyVersion: newPolicy.Version, Policy: newPolicy, PolicyHash: policyHash,
						SpecID: run.SpecID, SpecHash: run.SpecHash, RequestedModel: run.RequestedModel, ResolvedModel: run.ResolvedModel,
						ContentRevision: run.ContentRevision, ExpectedRevision: view.Revision, ExpectedTargetGeneration: handshake.Target.Generation,
						Automatic: classify.AutomaticFromProposals(after),
					})
					if err != nil {
						return fmt.Errorf("commit decision: %w", err)
					}
					output["committed"] = true
					output["receipt"] = receipt
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

// Keep the old command discoverable with explicit migration guidance.
func newRefreshSourceCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "refresh-source", Short: "Retired: update originals with the browser extension",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("source retrieval has been removed; open the original page and capture it with the Cairn browser extension")
		},
	}
	command.Flags().Int64("id", 0, "bookmark id (retired operation)")
	return command
}
