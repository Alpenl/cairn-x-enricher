package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/spf13/cobra"
)

func newClassifyCommand() *cobra.Command {
	var id int64
	var maxJobs int
	command := &cobra.Command{Use: "classify", Short: "Process saved sources with Jev, without X Search or reading generation", RunE: func(cmd *cobra.Command, _ []string) error {
		if maxJobs < 1 || maxJobs > 1000 || id < 0 {
			return fmt.Errorf("--max-jobs must be 1..1000 and --id must be nonnegative")
		}
		cfg, err := config.LoadFor(config.RoleClassify)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		httpClient := &http.Client{Timeout: cfg.RequestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, httpClient)
		catalog, legacyCatalog, err := queue.GetClassificationCatalog(ctx)
		if err != nil {
			return err
		}
		if legacyCatalog {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "backend has no v2 taxonomy; using the legacy classification vocabulary")
		}
		client, err := classify.NewClient(cfg.TypesafeBaseURL, cfg.TypesafeAPIKey, cfg.TypesafeModel, httpClient, catalog)
		if err != nil {
			return err
		}
		if id > 0 {
			if err := queue.RetryClassification(ctx, id); err != nil {
				return err
			}
		}
		if err := configureClassificationBudget(cfg, queue, client); err != nil {
			return err
		}
		worker := processor.NewStaged(queue, nil, client, catalog.Version, cfg.TypesafeModel, newLogger(cfg.LogLevel), 1)
		extensions := extensionService(cfg, client)
		extensions.SetBudgetStore(queue)
		extensions.SetRerankStore(queue)
		fetcher, policy := evidenceFetcher(cfg)
		worker.SetExtensions(extensions, fetcher, policy)
		completed, failed, err := worker.RunClassifications(ctx, maxJobs)
		if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]int64{"classified": completed, "failed": failed}); encodeErr != nil {
			return encodeErr
		}
		if err != nil {
			return err
		}
		if failed > 0 {
			return fmt.Errorf("%d classification job(s) failed", failed)
		}
		return nil
	}}
	command.Flags().Int64Var(&id, "id", 0, "enqueue a saved bookmark for reclassification before draining the queue")
	command.Flags().IntVar(&maxJobs, "max-jobs", 100, "maximum classification jobs to claim")
	return command
}
