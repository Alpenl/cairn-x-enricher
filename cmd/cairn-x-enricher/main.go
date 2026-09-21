package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func main() {
	_ = godotenv.Load()
	command := newRootCommand()
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, cfg, logger)
		},
	})

	var maxJobs int
	once := &cobra.Command{
		Use:   "once",
		Short: "Drain one bounded batch, print JSON stats, and exit",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
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
			worker, _, err := newProcessor(ctx, cfg, health.NewTracker(), logger)
			if err != nil {
				return err
			}
			stats, runErr := worker.Run(ctx, maxJobs)
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
	root.AddCommand(newReplayCommand())
	root.AddCommand(newRefreshSourceCommand())
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

func runServe(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	tracker := health.NewTracker()
	worker, queue, err := newProcessor(ctx, cfg, tracker, logger)
	if err != nil {
		return err
	}
	management := dashboard.New(ctx, tracker, queue, worker, logger, cfg.MaxConcurrency)
	management.SetExtensions(worker.Extensions())
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           management.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		// If this goroutine panicked, runServe would block forever waiting on
		// serverErrors while the process kept running without an HTTP server.
		defer processor.RecoverTask(logger, "health server")
		logger.Info("health server listening", "address", cfg.HTTPAddr)
		serverErrors <- server.ListenAndServe()
	}()
	// Track the scheduler so shutdown can wait for an in-flight batch. Without
	// this, main returns while a batch is still running and the process exits,
	// stranding every lease that batch holds.
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		runScheduler(ctx, worker, tracker, cfg, logger)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health server: %w", err)
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
) {
	// A batch can lease up to MAX_JOBS_PER_RUN jobs, so it must not inherit
	// the shutdown context directly. Cancelling mid-batch would strand every
	// already-leased job until its lease expires, wasting attempts.
	//
	// The batch budget is half the shutdown budget, leaving the other half for
	// runServe to wait out the same batch. Giving both phases the full budget
	// would exceed stop_grace_period, and Docker would SIGKILL the process
	// before either could finish.
	var mu sync.Mutex
	var batch sync.WaitGroup
	var stopping atomic.Bool

	run := func() {
		if stopping.Load() {
			return
		}
		mu.Lock()
		if stopping.Load() {
			mu.Unlock()
			return
		}
		batch.Add(1)
		mu.Unlock()
		defer batch.Done()

		// Recover per batch, not per scheduler: a panic must fail one batch and
		// drop readiness, but the loop has to keep running afterwards.
		stats, err := runBatchSafely(ctx, worker, cfg, logger)
		tracker.Record(stats, err)
		if err != nil && isContractFailure(err) {
			// A provider contract break will fail every future batch the
			// same way, so leave readiness false and stop pretending the
			// service is usable until an operator intervenes.
			tracker.MarkDegraded(err.Error())
		}
		attributes := []any{
			"claimed", stats.Claimed,
			"completed", stats.Completed,
			"failed", stats.Failed,
			"classified", stats.Classified,
			"classification_failed", stats.ClassificationFailed,
			"duration_ms", stats.Duration.Milliseconds(),
		}
		if err != nil {
			logger.ErrorContext(ctx, "scheduled batch failed", append(attributes, "error", err)...)
			return
		}
		logger.InfoContext(ctx, "scheduled batch finished", attributes...)
	}

	run()
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Stop admitting new batches, then let the in-flight one finish
			// within the shutdown budget.
			mu.Lock()
			stopping.Store(true)
			mu.Unlock()
			waitForBatch(&batch, batchTimeout(cfg), logger)
			return
		case <-ticker.C:
			run()
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
) (*processor.Processor, *cairn.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 20
	transport.MaxIdleConnsPerHost = 10
	httpClient := &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	queue := cairn.NewClient(cfg.CairnBaseURL, cfg.CairnToken, httpClient)
	// Prefer the multidimensional v2 vocabulary so the compiled questions cover
	// topics, content functions, carriers and affordances. A backend without the
	// v2 API falls back to the legacy vocabulary instead of failing to start.
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		if !cairn.IsUnsupported(err) {
			return nil, nil, fmt.Errorf("load Worker v2 taxonomy (requires curation backend migration): %w", err)
		}
		catalog, err = queue.GetTaxonomy(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("load Worker taxonomy (requires curation backend migration): %w", err)
		}
		logger.Warn("backend has no v2 taxonomy; running the legacy single-dimension vocabulary")
	}
	userAgent := "cairn-x-enricher/" + buildinfo.Version
	model := enrich.NewResponsesClient(
		cfg.GrokBaseURL,
		cfg.GrokAPIKey,
		cfg.GrokModel,
		cfg.GrokMaxTokens,
		userAgent,
		httpClient,
		catalog,
	)
	if _, err := model.Transform(ctx, enrich.Input{URL: "https://x.com/canary/status/0", Attempt: 1, SourceText: "Canary check: validate structured reading aids."}); err != nil {
		// A contract break must fail loudly at startup instead of silently
		// burning every job's retry budget.
		return nil, nil, fmt.Errorf("model endpoint contract check failed (check GROK_MODELS_BASE_URL, GROK_MODEL, XAI_API_KEY and strict schema support): %w", err)
	}
	classifier, err := classify.NewClient(cfg.TypesafeBaseURL, cfg.TypesafeAPIKey, cfg.TypesafeModel, httpClient, catalog)
	if err != nil {
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
	worker := processor.NewStaged(queue, model, classifier, catalog.Version, cfg.TypesafeModel, logger, cfg.MaxConcurrency)
	fetcher, policy := evidenceFetcher(cfg)
	worker.SetExtensions(extensionService(cfg, classifier), fetcher, policy)
	worker.SetPartialReuse(cfg.PartialReuse)
	return worker, queue, nil
}

// extensionService builds the bounded extension service from configuration.
// Every flag defaults off; the evidence fetcher is only constructed when the
// allowlist is non-empty, so an unconfigured process cannot fetch anything.
func extensionService(cfg config.Config, judge extension.Judge) *extension.Service {
	flags := extension.Flags{
		Entities: cfg.ExtensionEntities, Evidence: cfg.ExtensionEvidence,
		Rerank: cfg.ExtensionRerank, Proposal: cfg.ExtensionProposal,
	}
	service := extension.NewService(flags, extension.DefaultBudget(), judge)
	return service
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

// batchTimeout is the slice of the shutdown budget a single scheduled batch may
// consume. The other half is reserved for runServe to wait out that same batch,
// because both phases have to fit inside the container's stop_grace_period.
func batchTimeout(cfg config.Config) time.Duration {
	timeout := cfg.ShutdownTimeout / 2
	if timeout <= 0 {
		return cfg.ShutdownTimeout
	}
	return timeout
}

// runBatchSafely runs one scheduled batch, converting a panic into an error so
// the scheduler records a failure and continues instead of the process dying.
func runBatchSafely(
	ctx context.Context,
	worker *processor.Processor,
	cfg config.Config,
	logger *slog.Logger,
) (stats processor.Stats, err error) {
	defer processor.RecoverTask(logger, "scheduled batch")
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), batchTimeout(cfg))
	defer cancel()
	return worker.Run(runCtx, cfg.MaxJobsPerRun)
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
	levels := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: levels[level]}))
}
