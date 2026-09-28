package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/spf13/cobra"
)

// The provider GET validates a saved output, but only the Worker can commit it
// against the old lease, authoritative source, current content and R2 images.
func newProviderRecoverReadingCommand() *cobra.Command {
	var operationKey, actor string
	var commit bool
	command := &cobra.Command{
		Use:   "provider-recover-reading",
		Short: "Validate a ledger-bound saved reading; commit only when requested",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadFor(config.RoleEnrich)
			if err != nil {
				return err
			}
			operatorToken := strings.TrimSpace(os.Getenv("CAIRN_OPERATOR_TOKEN"))
			if operatorToken == "" || operatorToken == cfg.CairnToken ||
				operatorToken == strings.TrimSpace(os.Getenv("CAIRN_API_TOKEN")) {
				return errors.New("a distinct CAIRN_OPERATOR_TOKEN is required for provider recovery")
			}
			if commit && !validRecoveryActor(actor) {
				return errors.New("--actor must be a 3-80 character operator identifier")
			}
			client := &http.Client{Timeout: 20 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			operator := cairn.NewClient(cfg.CairnBaseURL, operatorToken, client)
			attempt, err := operator.InspectProviderAttempt(cmd.Context(), operationKey)
			if err != nil {
				return err
			}
			if attempt.Stage != "reading" || attempt.State != "responded" ||
				attempt.HTTPStatus == nil || *attempt.HTTPStatus != http.StatusOK ||
				attempt.ResponseID == nil || attempt.LinkID == nil || attempt.ContentRevision == nil ||
				attempt.CurrentContentRevision == nil || *attempt.ContentRevision != *attempt.CurrentContentRevision {
				return errors.New("provider permit has no current, bound, settled reading response")
			}
			internal := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, client)
			source, err := internal.GetSource(cmd.Context(), *attempt.LinkID)
			if err != nil {
				return err
			}
			if source == nil || strings.TrimSpace(source.OriginalText) == "" {
				return errors.New("current source snapshot is unavailable")
			}
			summary, reading, err := enrich.RetrieveStoredReading(cmd.Context(), cfg.GrokBaseURL,
				cfg.GrokAPIKey, *attempt.ResponseID, *source, client)
			if err != nil {
				return err
			}
			if summary.Model != attempt.Model || reading.Model != attempt.Model || summary.Status != "completed" {
				return errors.New("stored provider reading does not match the ledger model and status")
			}
			result := struct {
				Phase          string `json:"phase"`
				SourceImages   int    `json:"source_images"`
				ContentVersion int64  `json:"content_revision,omitempty"`
			}{Phase: "validated", SourceImages: len(source.ImageURLs)}
			if commit {
				receipt, err := operator.RecoverProviderReading(cmd.Context(), operationKey,
					*attempt.ResponseID, actor, reading)
				if err != nil {
					return err
				}
				if receipt.ID != *attempt.LinkID || receipt.ContentRevision != *attempt.ContentRevision {
					return errors.New("provider reading receipt does not match the intended bookmark")
				}
				result.Phase = "committed"
				result.ContentVersion = receipt.ContentRevision
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	command.Flags().StringVar(&operationKey, "operation-key", "", "exact 64-character Worker permit key")
	command.Flags().StringVar(&actor, "actor", "", "operator identifier recorded in the private audit")
	command.Flags().BoolVar(&commit, "commit", false, "commit verified reading through Worker recovery")
	_ = command.MarkFlagRequired("operation-key")
	return command
}
