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

// A successful provider GET cannot by itself authorize a business write. The
// Worker receipt must already bind the ID to a settled fetch attempt, and its
// recovery transaction fences the original content revision and lease.
func newProviderRecoverSourceCommand() *cobra.Command {
	var operationKey, actor string
	var commit bool
	command := &cobra.Command{
		Use:   "provider-recover-source",
		Short: "Validate a ledger-bound stored xAI source; commit only when requested",
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
			worker := cairn.NewClient(cfg.CairnBaseURL, operatorToken, client)
			attempt, err := worker.InspectProviderAttempt(cmd.Context(), operationKey)
			if err != nil {
				return err
			}
			if attempt.Stage != "fetch" || attempt.State != "responded" ||
				attempt.HTTPStatus == nil || *attempt.HTTPStatus != http.StatusOK || attempt.ResponseID == nil ||
				attempt.LinkID == nil || attempt.ContentRevision == nil {
				return errors.New("provider permit has no bound, settled source response")
			}
			summary, source, err := enrich.RetrieveStoredSource(cmd.Context(), cfg.GrokBaseURL,
				cfg.GrokAPIKey, *attempt.ResponseID, client)
			if err != nil {
				return err
			}
			if summary.Model != attempt.Model || source.Model != attempt.Model || summary.Status != "completed" {
				return errors.New("stored provider source does not match the ledger model and status")
			}
			result := struct {
				Phase          string `json:"phase"`
				SourceBytes    int    `json:"source_bytes,omitempty"`
				ContextBytes   int    `json:"context_bytes,omitempty"`
				RelatedLinks   int    `json:"related_links,omitempty"`
				Images         int    `json:"images,omitempty"`
				ContentVersion int64  `json:"content_revision,omitempty"`
			}{Phase: "validated", SourceBytes: len(source.OriginalText),
				ContextBytes: len(source.ContextText), RelatedLinks: len(source.RelatedLinks),
				Images: len(source.ImageURLs)}
			if commit {
				receipt, err := worker.RecoverProviderSource(cmd.Context(), operationKey,
					*attempt.ResponseID, actor, source)
				if err != nil {
					return err
				}
				if receipt.ID != *attempt.LinkID {
					return errors.New("provider recovery receipt refers to another bookmark")
				}
				result.Phase = "committed"
				result.ContentVersion = receipt.ContentRevision
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	command.Flags().StringVar(&operationKey, "operation-key", "", "exact 64-character Worker permit key")
	command.Flags().StringVar(&actor, "actor", "", "operator identifier recorded in the private audit")
	command.Flags().BoolVar(&commit, "commit", false, "commit the verified source through the Worker recovery transaction")
	_ = command.MarkFlagRequired("operation-key")
	return command
}

func validRecoveryActor(actor string) bool {
	if len(actor) < 3 || len(actor) > 80 {
		return false
	}
	for _, char := range actor {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '@' || char == '-' {
			continue
		}
		return false
	}
	return true
}
