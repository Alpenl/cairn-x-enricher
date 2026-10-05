package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/collectionorganize"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
	"github.com/Alpenl/cairn-x-enricher/internal/presentation"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func main() {
	_ = godotenv.Load()
	if executeCommand(newRootCommand(), os.Stderr) != 0 {
		os.Exit(1)
	}
}

func executeCommand(command *cobra.Command, stderr io.Writer) int {
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", observability.SafeErrorCode(err))
		return 1
	}
	return 0
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "cairn-x-enricher",
		Short:         "Enrich saved X links with verified source text and summaries",
		Version:       buildinfo.Current().Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("cairn-x-enricher {{.Version}}\n")

	root.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run the scheduler and health server",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.LogLevel)
			var observer *observability.Store
			var closeLogs func(context.Context) error
			if cfg.ObservabilityConfigPath != "" {
				observer, err = observability.Open(cfg.ObservabilityConfigPath, logLevel(cfg.LogLevel))
				if err != nil {
					return err
				}
				logger, closeLogs, err = observer.AsyncLogger(os.Stderr, 1024)
				if err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			serveErr := runServe(ctx, cfg, logger, observer)
			if closeLogs != nil {
				// Diagnostics never delay shutdown indefinitely or change the
				// business result if stderr or an optional collector is blocked.
				drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = closeLogs(drainCtx)
				cancel()
			}
			return serveErr
		},
	})

	var maxJobs int
	once := &cobra.Command{
		Use:   "once",
		Short: "Drain one bounded batch, print JSON stats, and exit",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.LoadFor(config.RoleClassify)
			if err != nil {
				return err
			}
			if maxJobs == 0 {
				maxJobs = cfg.MaxJobsPerRun
			}
			if maxJobs < 1 || maxJobs > 1000 {
				return errors.New("--max-jobs must be between 1 and 1000")
			}
			logger := newLogger(cfg.LogLevel)
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			// This is a one-shot snapshot. If there is no claimable source now,
			// this invocation runs only classification. A source arriving after
			// the check waits for the next invocation; it cannot be claimed
			// without the reading contract canary.
			probe := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken,
				upstreamHTTPClient(cfg.WorkerRequestTimeout))
			sourceClaimable, probeErr := probe.SourceClaimable(ctx)
			if probeErr != nil {
				// Older or temporarily unavailable Workers cannot prove the
				// source queue empty. Preserve the eager canary in that case.
				logger.Warn("source claimability check failed; using full startup check",
					"error", probeErr)
				sourceClaimable = true
			}
			if sourceClaimable {
				cfg, err = config.LoadFor(config.RoleServe)
				if err != nil {
					return err
				}
			}
			worker, _, err := newProcessor(ctx, cfg, health.NewTracker(), logger, sourceClaimable)
			if err != nil {
				return err
			}
			var stats processor.Stats
			var runErr error
			if sourceClaimable {
				stats, runErr = worker.Run(ctx, maxJobs)
			} else {
				stats, runErr = worker.RunClassificationsOnly(ctx, maxJobs)
			}
			if err := json.NewEncoder(os.Stdout).Encode(stats); err != nil {
				return fmt.Errorf("write stats: %w", err)
			}
			if runErr != nil {
				return runErr
			}
			if stats.Failed > 0 || stats.ClassificationFailed > 0 {
				return fmt.Errorf("%d enrichment and %d classification job(s) failed", stats.Failed, stats.ClassificationFailed)
			}
			return nil
		},
	}
	once.Flags().IntVar(&maxJobs, "max-jobs", 0, "maximum jobs to claim (default MAX_JOBS_PER_RUN)")
	root.AddCommand(once)
	root.AddCommand(newClassifyCommand())
	root.AddCommand(newObserveCommand())
	root.AddCommand(newReplayCommand())
	root.AddCommand(newRefreshSourceCommand())
	root.AddCommand(newProviderInspectCommand())
	root.AddCommand(newProviderRecoverSourceCommand())
	root.AddCommand(newProviderRecoverReadingCommand())
	root.AddCommand(newExportDatasetCommand())

	var healthURL string
	var healthTimeout time.Duration
	var healthReady bool
	healthcheck := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check a running service's liveness or readiness endpoint",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				if healthReady {
					return fmt.Errorf("readiness endpoint returned HTTP %d: %s", response.StatusCode, readinessReason(response.Body))
				}
				return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
			}
			return nil
		},
	}
	healthcheck.Flags().StringVar(&healthURL, "url", "http://127.0.0.1:8080/healthz", "liveness endpoint URL")
	healthcheck.Flags().DurationVar(&healthTimeout, "timeout", 3*time.Second, "request timeout")
	healthcheck.Flags().BoolVar(&healthReady, "ready", false, "check readiness by defaulting the URL to /readyz")
	healthcheck.PreRunE = func(_ *cobra.Command, _ []string) error {
		if healthReady && !healthcheck.Flags().Changed("url") {
			healthURL = "http://127.0.0.1:8080/readyz"
		}
		return nil
	}
	root.AddCommand(healthcheck)

	var versionJSON bool
	version := &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		RunE: func(_ *cobra.Command, _ []string) error {
			info := buildinfo.Current()
			if versionJSON {
				return json.NewEncoder(os.Stdout).Encode(info)
			}
			_, err := fmt.Fprintf(os.Stdout, "%s (%s, %s, %s/%s)\n", info.Version, info.Commit, info.Date, runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
	version.Flags().BoolVar(&versionJSON, "json", false, "emit a stable JSON object")
	root.AddCommand(version)
	return root
}

func runServe(ctx context.Context, cfg config.Config, logger *slog.Logger, observer *observability.Store) error {
	var controlListener net.Listener
	if observer != nil {
		var listenErr error
		controlListener, listenErr = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ObservabilityControlAddr)
		if listenErr != nil {
			return fmt.Errorf("listen on observability control loopback: %w", listenErr)
		}
		defer func() { _ = controlListener.Close() }()
	}
	tracker := health.NewTracker()
	worker, queue, err := newProcessor(ctx, cfg, tracker, logger, true)
	if err != nil {
		return err
	}
	management := dashboard.New(ctx, tracker, queue, worker, logger, cfg.MaxConcurrency)
	management.SetFormattingEnabled(cfg.FormatModel != "" && cfg.FormatBaseURL != "" && cfg.FormatAPIKey != "")
	management.SetExtensions(worker.Extensions())
	if observer != nil {
		management.SetObservabilityStatus(observer.Snapshot)
	}
	wakeup := make(chan struct{}, 1)
	management.SetWakeup(wakeup)
	classificationWakeup := make(chan struct{}, 1)
	management.SetClassificationWakeup(classificationWakeup)
	worker.SetClassificationWakeup(classificationWakeup)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           management.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var controlServer *http.Server
	var controlErrors chan error
	if observer != nil {
		auditCtx, stopAudit := context.WithCancel(ctx)
		defer stopAudit()
		go func() {
			superviseLane(auditCtx, tracker, logger, "policy_publisher", func(ctx context.Context) {
				runWorkerPolicyPublisher(ctx, observer, queue, tracker)
			}, defaultRestartPolicy())
		}()
		controlServer = &http.Server{Handler: observer.Handler(), ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		controlErrors = make(chan error, 1)
		go func() {
			controlErrors <- criticalServerResult(func() error { return controlServer.Serve(controlListener) })
		}()
		go func() {
			superviseLane(auditCtx, tracker, logger, "audit_pruning", func(ctx context.Context) {
				runMonitoredPeriodic(ctx, tracker, "audit_pruning", time.Hour, time.Minute, func() {
					_ = observer.PruneAudit() // failures remain visible in control_audit_errors while logs are off
				})
			}, defaultRestartPolicy())
		}()
	}

	serverErrors := make(chan error, 1)
	go func() {
		// If this goroutine panicked, runServe would block forever waiting on
		// serverErrors while the process kept running without an HTTP server.
		serverErrors <- criticalServerResult(func() error {
			logger.Info("health server listening", "address", cfg.HTTPAddr)
			return server.ListenAndServe()
		})
	}()
	// Track the scheduler so shutdown can wait for an in-flight batch. Without
	// this, main returns while a batch is still running and the process exits,
	// stranding every lease that batch holds.
	if cfg.FormatModel != "" && cfg.FormatBaseURL != "" && cfg.FormatAPIKey != "" {
		formatter := &presentation.Runner{Config: presentation.Config{WorkerURL: cfg.CairnBaseURL, WorkerToken: cfg.CairnToken, BaseURL: cfg.FormatBaseURL, APIKey: cfg.FormatAPIKey, Model: cfg.FormatModel, Auto: cfg.FormatAuto, DailyLimit: cfg.FormatDailyLimit}, Client: upstreamHTTPClient(120 * time.Second)}
		go formatter.Run(ctx, logger)
	}
	collectionJudge, err := classify.NewJudgeClient(cfg.TypesafeBaseURL, cfg.TypesafeAPIKey, cfg.TypesafeModel, upstreamHTTPClient(cfg.TypesafeRequestTimeout))
	if err != nil {
		return err
	}
	organizer := &collectionorganize.Runner{Queue: queue, Judge: collectionJudge, Limits: extension.ReservationLimits{MaxCallsTotal: cfg.ExtensionMaxCalls, MaxCallsPerItem: cfg.ExtensionMaxCallsPerItem, MaxTokens: cfg.ExtensionMaxInputTokens, MaxTokensPerItem: cfg.ExtensionMaxInputTokensPerItem}}
	organizerDone := make(chan struct{})
	go func() { defer close(organizerDone); organizer.Run(ctx, logger) }()
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		runScheduler(ctx, worker, tracker, cfg, logger, wakeup, classificationWakeup)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health server: %w", err)
		}
	case err := <-controlErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("observability control server: %w", err)
		}
	}

	// Shutdown must fit inside one deadline: the HTTP server drain and the
	// in-flight job drain share a single budget, because Docker sends SIGKILL
	// once stop_grace_period elapses and would otherwise cut the second phase
	// short partway through.
	//
	// The context is deliberately rooted at Background rather than at ctx:
	// reaching this point means ctx has already been cancelled, so inheriting
	// from it would make Shutdown return immediately and abandon every open
	// connection. The deadline below is what bounds this work.
	deadline := time.Now().Add(cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	//nolint:contextcheck // shutdown must outlive the already-cancelled signal context
	serverErr := server.Shutdown(shutdownCtx)
	if controlServer != nil {
		//nolint:contextcheck // Both listeners share the one shutdown deadline.
		serverErr = errors.Join(serverErr, controlServer.Shutdown(shutdownCtx))
	}
	// Drain in-flight jobs even when the HTTP server did not stop cleanly. A
	// client streaming an image can outlive the HTTP deadline, and returning
	// early here would abandon leased jobs - the opposite of the intent.
	if remaining := time.Until(deadline); remaining > 0 {
		management.Drain(remaining)
	}
	// Wait for the scheduler to finish its in-flight batch within the same
	// budget. runScheduler caps its own wait, so this cannot block past the
	// deadline; the extra bound here is defensive.
	if remaining := time.Until(deadline); remaining > 0 {
		if !waitForSignal(schedulerDone, remaining) {
			logger.Warn("scheduler did not stop within the shutdown budget; " +
				"its leased jobs keep their lease and will be retried")
		}
	}
	if remaining := time.Until(deadline); remaining > 0 && !waitForSignal(organizerDone, remaining) {
		logger.Warn("collection organizer did not stop within the shutdown budget; unknown results require review")
	}
	if serverErr != nil {
		return fmt.Errorf("shutdown health server: %w", serverErr)
	}
	return nil
}

func runScheduler(
	ctx context.Context,
	worker *processor.Processor,
	tracker *health.Tracker,
	cfg config.Config,
	logger *slog.Logger,
	wakeup ...<-chan struct{},
) {
	var notified <-chan struct{}
	var classificationNotified <-chan struct{}
	if len(wakeup) > 0 {
		notified = wakeup[0]
	}
	if len(wakeup) > 1 {
		classificationNotified = wakeup[1]
	}
	var loops sync.WaitGroup
	loops.Add(3)
	go func() {
		defer loops.Done()
		superviseLane(ctx, tracker, logger, "source", func(ctx context.Context) {
			runSourceScheduler(ctx, worker, tracker, cfg, logger, notified)
		}, defaultRestartPolicy())
	}()
	go func() {
		defer loops.Done()
		superviseLane(ctx, tracker, logger, "classification", func(ctx context.Context) {
			runClassificationScheduler(ctx, worker, tracker, cfg, logger, classificationNotified)
		}, defaultRestartPolicy())
	}()
	go func() {
		defer loops.Done()
		superviseLane(ctx, tracker, logger, "evidence", func(ctx context.Context) {
			runEvidenceSchedulerTracked(ctx, worker, tracker, cfg, logger)
		}, defaultRestartPolicy())
	}()
	<-ctx.Done()
	// Each loop stops claiming on cancellation. Already leased jobs retain
	// their own bounded stage contexts; wait within the shared shutdown budget.
	waitForBatch(&loops, batchTimeout(cfg), logger)
}

func runSourceScheduler(ctx context.Context, worker *processor.Processor, tracker *health.Tracker, cfg config.Config, logger *slog.Logger, notified <-chan struct{}) {
	ctx = processor.WithLaneProgress(ctx, func(grace time.Duration) func() { return tracker.BeginLaneWork("source", grace) })
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		tracker.PulseLane("source", "running", cfg.WorkerRequestTimeout+30*time.Second)
		stats, err := runSourceSweepSafely(ctx, worker, cfg, logger)
		tracker.Record(stats, err)
		if err != nil && isContractFailure(err) {
			tracker.MarkComponentDegraded("source_runtime", err.Error())
		} else if err == nil && stats.Completed > 0 {
			tracker.MarkComponentRecovered("source_runtime")
		}
		for _, stage := range []string{"source", "reading"} {
			if paused, reason, _ := worker.SourceStagePaused(stage); paused {
				tracker.MarkComponentDegraded(stage, reason)
			} else {
				tracker.MarkComponentRecovered(stage)
			}
		}
		attributes := []any{"stage", "source", "claimed", stats.Claimed, "completed", stats.Completed, "failed", stats.Failed, "duration_ms", stats.Duration.Milliseconds()}
		if err != nil {
			logger.ErrorContext(ctx, "scheduled batch failed", append(attributes, "error", err)...)
		} else {
			logger.InfoContext(ctx, "scheduled batch finished", attributes...)
		}
		tracker.PulseLane("source", "waiting", waitingGrace(cfg))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-notified:
		}
	}
}

func runClassificationScheduler(ctx context.Context, worker *processor.Processor, tracker *health.Tracker, cfg config.Config, logger *slog.Logger, wakeup ...<-chan struct{}) {
	ctx = processor.WithLaneProgress(ctx, func(grace time.Duration) func() { return tracker.BeginLaneWork("classification", grace) })
	var notified <-chan struct{}
	if len(wakeup) > 0 {
		notified = wakeup[0]
	}
	// Create the ticker before the first round: a long initial drain cannot
	// postpone the next scheduling opportunity by another full interval.
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		tracker.PulseLane("classification", "running", cfg.WorkerRequestTimeout+30*time.Second)
		stats, err := runClassificationSafely(ctx, worker, cfg, logger)
		tracker.RecordClassification(stats, err)
		if err != nil && isContractFailure(err) {
			tracker.MarkComponentDegraded("classification", err.Error())
		} else if err == nil && stats.Classified > 0 {
			tracker.MarkComponentRecovered("classification")
		}
		attributes := []any{"classified", stats.Classified, "failed", stats.ClassificationFailed, "duration_ms", stats.Duration.Milliseconds()}
		if err != nil {
			logger.WarnContext(ctx, "classification round stopped", append(attributes, "error", err)...)
		} else {
			logger.InfoContext(ctx, "classification round finished", attributes...)
		}
		tracker.PulseLane("classification", "waiting", waitingGrace(cfg))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-notified:
		}
	}
}

func runEvidenceSchedulerTracked(ctx context.Context, worker *processor.Processor, tracker *health.Tracker, cfg config.Config, _ *slog.Logger) {
	if tracker != nil {
		ctx = processor.WithLaneProgress(ctx, func(grace time.Duration) func() { return tracker.BeginLaneWork("evidence", grace) })
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if tracker != nil {
			tracker.PulseLane("evidence", "running", cfg.WorkerRequestTimeout+30*time.Second)
		}
		worker.RunEvidenceRecovery(ctx, cfg.MaxJobsPerRun)
		if tracker != nil {
			tracker.PulseLane("evidence", "waiting", waitingGrace(cfg))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// waitForBatch blocks until the in-flight batch finishes, but never longer than
// timeout.
//
// A batch is normally bounded by its own context, but that only holds while
// every stage honours cancellation. If any stage ever blocks past its context,
// an unbounded Wait here would keep the process alive forever and the container
// would ignore SIGTERM until it was killed, so the wait is capped as well.
func waitForBatch(batch *sync.WaitGroup, timeout time.Duration, logger *slog.Logger) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		batch.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		logger.Warn("in-flight batch did not finish within the shutdown budget; "+
			"its leased jobs keep their lease and will be retried", "timeout", timeout)
	}
}

func newProcessor(
	ctx context.Context,
	cfg config.Config,
	tracker *health.Tracker,
	logger *slog.Logger,
	withSource bool,
) (*processor.Processor, *cairn.Client, error) {
	queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, upstreamHTTPClient(cfg.WorkerRequestTimeout))
	catalog, legacyCatalog, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		return nil, nil, err
	}
	if legacyCatalog {
		logger.Warn("backend has no v2 taxonomy; running the legacy single-dimension vocabulary")
	}
	var reader processor.SourceReader
	var preflight func(context.Context) error
	var preflightErr error
	var checkScope string
	if withSource {
		model := enrich.NewResponsesClient(
			cfg.GrokBaseURL, cfg.GrokAPIKey, cfg.GrokModel, cfg.GrokMaxTokens,
			"cairn-x-enricher/"+buildinfo.Version, upstreamHTTPClient(cfg.GrokFetchTimeout), catalog,
		)
		model.SetReadingHTTPClient(upstreamHTTPClient(cfg.GrokReadingTimeout))
		model.SetPaidAttemptLedger(queue)
		model.SetLogger(logger)
		// Key changes invalidate cached success, but the secret itself is never stored.
		checkScope = model.ReadingCheckScope()
		preflight = func(ctx context.Context) error {
			if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
				return err
			}
			check, err := queue.ProviderCheck(ctx, checkScope, "claim", nil)
			if err != nil {
				return err
			}
			if check.State == "healthy" {
				return nil
			}
			if !check.Granted {
				return &processor.PreflightRetry{At: time.UnixMilli(check.NextCheckAt)}
			}
			_, err = model.Transform(ctx, enrich.Input{URL: "https://x.com/canary/status/0", Attempt: 1,
				SourceText: "Canary check: validate structured reading aids.", Canary: true})
			reason := providerCheckReason(err)
			// Settlement is bounded separately so a timed-out provider doesn't strand its check.
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.WorkerRequestTimeout)
			defer cancel()
			final, finishErr := queue.ProviderCheck(finishCtx, checkScope, "finish", map[string]any{
				"lease_token": check.LeaseToken, "success": err == nil, "reason": reason})
			if finishErr != nil {
				return finishErr
			}
			if err != nil {
				return &processor.PreflightRetry{At: time.UnixMilli(final.NextCheckAt)}
			}
			return nil
		}
		preflightErr = processor.ErrSourcePreflightUnverified
		reader = model
	}
	classifier, err := classify.NewClient(cfg.TypesafeBaseURL, cfg.TypesafeAPIKey, cfg.TypesafeModel,
		upstreamHTTPClient(cfg.TypesafeRequestTimeout), catalog)
	if err != nil {
		return nil, nil, err
	}
	classifier, err = configureClassificationCandidates(ctx, cfg, queue, classifier)
	if err != nil {
		return nil, nil, err
	}
	if err := configureClassificationBudget(cfg, queue, classifier); err != nil {
		return nil, nil, err
	}
	// Register the immutable question spec so a stored run can be replayed
	// against the exact definition it was evaluated with. Re-registering the
	// same bytes is idempotent; a changed definition under the same id is
	// rejected by the Worker, which is why the spec id changes with semantics.
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		if !cairn.IsUnsupported(err) {
			return nil, nil, fmt.Errorf("register classification question spec: %w", err)
		}
		logger.Warn("backend has no v2 question-spec endpoint; stored runs will not be replayable")
	}
	tracker.MarkStarted()
	worker := processor.NewStaged(queue, reader, classifier, catalog.Version, cfg.TypesafeModel, logger, cfg.MaxConcurrency)
	worker.SetSourcePreflight(preflight, preflightErr)
	if withSource {
		worker.SetSourceRecovery(func(ctx context.Context) (cairn.ProviderCheckStatus, error) {
			return queue.ProviderCheck(ctx, checkScope, "status", nil)
		},
			func(ctx context.Context) (cairn.ProviderCheckStatus, error) {
				return queue.ProviderCheck(ctx, checkScope, "recover", nil)
			})
	}
	if preflightErr != nil {
		tracker.MarkComponentDegraded("source", "source provider contract check pending")
		tracker.MarkComponentDegraded("reading", "source provider contract check pending")
	}
	worker.SetClaimTimeout(batchTimeout(cfg))
	worker.SetPaidStageTimeout(max(cfg.GrokFetchTimeout, cfg.GrokReadingTimeout))
	fetcher, policy := evidenceFetcher(cfg)
	extensions, err := extensionService(cfg, classifier)
	if err != nil {
		return nil, nil, err
	}
	extensions.SetBudgetStore(queue)
	extensions.SetRerankStore(queue)
	extensions.SetEntityStore(queue)
	worker.SetExtensions(extensions, fetcher, policy)
	worker.SetPartialReuse(cfg.PartialReuse)
	return worker, queue, nil
}

// extensionService builds the bounded extension service from configuration.
// Every flag defaults off; the evidence fetcher is only constructed when the
// allowlist is non-empty, so an unconfigured process cannot fetch anything.
func extensionService(cfg config.Config, judge extension.Judge) (*extension.Service, error) {
	flags := extension.Flags{
		Entities: cfg.ExtensionEntities, Evidence: cfg.ExtensionEvidence,
		Rerank: cfg.ExtensionRerank, Proposal: cfg.ExtensionProposal,
	}
	budget := extension.DefaultBudget()
	if cfg.ExtensionMaxCalls > 0 {
		budget.MaxCallsTotal = cfg.ExtensionMaxCalls
	}
	if cfg.ExtensionMaxCallsPerItem > 0 {
		budget.MaxCallsPerItem = cfg.ExtensionMaxCallsPerItem
	}
	if cfg.ExtensionMaxInputTokens > 0 {
		budget.MaxTokens = cfg.ExtensionMaxInputTokens
	}
	if cfg.ExtensionMaxInputTokensPerItem > 0 {
		budget.MaxTokensPerItem = cfg.ExtensionMaxInputTokensPerItem
	}
	if cfg.ExtensionTimeout > 0 {
		budget.Timeout = cfg.ExtensionTimeout
	}
	service := extension.NewService(flags, budget, judge)
	if cfg.EntityCatalog.Version != "" {
		if err := service.SetEntityCatalog(cfg.EntityCatalog); err != nil {
			return nil, err
		}
	}
	return service, nil
}

// evidenceFetcher returns a controlled HTTP client for the evidence extension,
// or nil when no host is allowlisted.
func evidenceFetcher(cfg config.Config) (*http.Client, extension.FetchPolicy) {
	policy := extension.DefaultFetchPolicy(cfg.ExtensionAllowlist)
	if len(policy.AllowedHosts) == 0 {
		return nil, policy
	}
	client, err := extension.ControlledFetcher(policy, nil)
	if err != nil {
		return nil, policy
	}
	return client, policy
}

// waitForSignal reports whether done was closed within timeout.
func waitForSignal(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// batchTimeout bounds one claim request and the scheduler shutdown wait. It
// does not limit how long a source or classification round can keep claiming.
func batchTimeout(cfg config.Config) time.Duration {
	timeout := cfg.ShutdownTimeout / 2
	if timeout <= 0 {
		return cfg.ShutdownTimeout
	}
	return timeout
}

// runSourceSweepSafely follows each full batch immediately while backlog
// remains. Each claim has its own timeout; shutdown stops new claims without
// cutting off already leased paid work.
func runSourceSweepSafely(ctx context.Context, worker *processor.Processor, cfg config.Config, logger *slog.Logger) (stats processor.Stats, err error) {
	defer processor.RecoverJob(logger, "scheduled source sweep", 0, &err)
	stats.StartedAt = time.Now().UTC()
	for ctx.Err() == nil {
		part, runErr := worker.RunSources(ctx, cfg.MaxJobsPerRun)
		stats.Claimed += part.Claimed
		stats.Completed += part.Completed
		stats.Failed += part.Failed
		if runErr != nil {
			err = runErr
			break
		}
		if cfg.MaxJobsPerRun <= 0 || part.Claimed < int64(cfg.MaxJobsPerRun) || part.Completed+part.Failed == 0 {
			break
		}
	}
	stats.Duration = time.Since(stats.StartedAt)
	return stats, err
}

func runClassificationSafely(ctx context.Context, worker *processor.Processor, cfg config.Config, logger *slog.Logger) (stats processor.Stats, err error) {
	defer processor.RecoverJob(logger, "scheduled classification round", 0, &err)
	stats.StartedAt = time.Now().UTC()
	stats.Classified, stats.ClassificationFailed, err = worker.RunClassifications(ctx, cfg.MaxJobsPerRun)
	stats.Duration = time.Since(stats.StartedAt)
	return stats, err
}

// readinessReason extracts the human-readable reason from a /readyz body so a
// failing container healthcheck explains itself in `docker inspect`.
func readinessReason(body io.Reader) string {
	var payload struct {
		Reason string `json:"ready_reason"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 8<<10)).Decode(&payload); err != nil || payload.Reason == "" {
		return "not ready"
	}
	return payload.Reason
}

// isContractFailure reports whether an error is a configuration or upstream
// contract fault that retrying cannot repair.
// isContractFailure reports whether a batch failure means the service cannot
// make progress until an operator changes configuration or the provider fixes
// its contract. Transient network/rate-limit faults and stale/conflict
// responses are excluded: those are handled by bounded retry and must not drop
// readiness for every future batch.
func isContractFailure(err error) bool {
	if err == nil {
		return false
	}
	// The typed classification is authoritative when present.
	class := enrich.ClassOf(enrich.ClassifyModelError(err))
	if class == enrich.ErrorClassConfiguration || class == enrich.ErrorClassContract {
		return true
	}
	var modelErr *enrich.ModelHTTPError
	if errors.As(err, &modelErr) {
		switch modelErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest:
			return true
		}
	}
	var apiErr *cairn.APIError
	if errors.As(err, &apiErr) {
		// The Worker's typed code is authoritative: capability_mismatch and
		// configuration_error are component faults, while target_changed,
		// input_changed and lease_expired are stale jobs that must not drop
		// readiness.
		switch apiErr.Class() {
		case enrich.ErrorClassConfiguration, enrich.ErrorClassContract:
			return true
		default:
			return false
		}
	}
	return false
}

func newLogger(level string) *slog.Logger {
	return slog.New(observability.SafeJSONHandler(os.Stderr, logLevel(level)))
}

func logLevel(level string) slog.Level {
	levels := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	return levels[level]
}

// configureClassificationCandidates leaves full coverage as the default.
// Explicit candidate mode is validated and negotiated before any model call.
func configureClassificationCandidates(ctx context.Context, cfg config.Config, queue *cairn.Client, client *classify.Client) (*classify.Client, error) {
	if cfg.ClassificationCandidateMaxQuestions == 0 {
		return client, nil
	}
	candidate, err := client.WithCandidatePolicy(classify.CandidatePolicy{
		Version: classify.CandidatePolicyVersion, MaxQuestions: cfg.ClassificationCandidateMaxQuestions,
	})
	if err != nil {
		return nil, fmt.Errorf("configure classification candidates: %w", err)
	}
	if err := queue.ProbeCandidateManifestCapability(ctx); err != nil {
		return nil, fmt.Errorf("candidate manifest capability: %w", err)
	}
	return candidate, nil
}

// configureClassificationBudget wires persistent admission before workers run.
func configureClassificationBudget(cfg config.Config, queue *cairn.Client, client *classify.Client) error {
	limits := classify.DefaultCallBudgetLimits()
	if cfg.ClassificationMaxCalls > 0 {
		limits.MaxCallsTotal = cfg.ClassificationMaxCalls
	}
	if cfg.ClassificationMaxCallsPerItem > 0 {
		limits.MaxCallsPerItem = cfg.ClassificationMaxCallsPerItem
	}
	if cfg.ClassificationMaxInputTokens > 0 {
		limits.MaxTokens = cfg.ClassificationMaxInputTokens
	}
	if cfg.ClassificationMaxInputTokensPerItem > 0 {
		limits.MaxTokensPerItem = cfg.ClassificationMaxInputTokensPerItem
	}
	if err := queue.SetClassificationBudgetLimits(limits); err != nil {
		return err
	}
	return client.SetCallBudget(queue, limits)
}
