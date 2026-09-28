package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/spf13/cobra"
)

// This command is intentionally read-only. A provider GET can establish that
// an ID resolves, but cannot prove billing or associate an externally supplied
// ID with a reserved Worker permit. It never settles a permit or requeues a job.
func newProviderInspectCommand() *cobra.Command {
	var operationKey, responseID string
	command := &cobra.Command{
		Use:   "provider-inspect",
		Short: "Inspect one paid-attempt permit and optionally retrieve a stored xAI response",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadFor(config.RoleEnrich)
			if err != nil {
				return err
			}
			operatorToken := strings.TrimSpace(os.Getenv("CAIRN_OPERATOR_TOKEN"))
			if operatorToken == "" || operatorToken == cfg.CairnToken ||
				operatorToken == strings.TrimSpace(os.Getenv("CAIRN_API_TOKEN")) {
				return errors.New("a distinct CAIRN_OPERATOR_TOKEN is required for provider inspection")
			}
			client := &http.Client{Timeout: 20 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			worker := cairn.NewClient(cfg.CairnBaseURL, operatorToken, client)
			attempt, err := worker.InspectProviderAttempt(cmd.Context(), operationKey)
			if err != nil {
				return fmt.Errorf("inspect provider attempt: %w", err)
			}
			report := struct {
				Attempt        cairn.ProviderAttemptInspection `json:"attempt"`
				ProviderLookup string                          `json:"provider_lookup"`
				Association    string                          `json:"association"`
				Billing        string                          `json:"billing"`
				Response       *enrich.StoredResponseSummary   `json:"response,omitempty"`
			}{Attempt: attempt, ProviderLookup: "not_attempted_no_response_id",
				Association: "unverified", Billing: "not_determined_by_lookup"}
			lookupID := responseID
			if attempt.ResponseID != nil {
				if responseID != "" && responseID != *attempt.ResponseID {
					return errors.New("supplied response ID differs from the stored ledger ID")
				}
				lookupID = *attempt.ResponseID
				report.Association = "ledger_response_id_match"
			}
			if lookupID != "" {
				response, err := enrich.RetrieveStoredResponse(cmd.Context(), cfg.GrokBaseURL,
					cfg.GrokAPIKey, lookupID, client)
				if errors.Is(err, enrich.ErrStoredResponseNotFound) {
					report.ProviderLookup = "not_found_billing_unknown"
				} else if err != nil {
					return err
				} else {
					report.ProviderLookup = "found"
					report.Response = &response
					if attempt.State == "confirmed_not_billed" {
						report.Association = "contradicts_confirmed_not_billed"
					} else if response.Model != attempt.Model {
						report.Association = "model_mismatch"
					}
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		},
	}
	command.Flags().StringVar(&operationKey, "operation-key", "", "exact 64-character Worker permit key")
	command.Flags().StringVar(&responseID, "response-id", "", "optional xAI response ID from independent evidence")
	_ = command.MarkFlagRequired("operation-key")
	return command
}
