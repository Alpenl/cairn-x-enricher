package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
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
		SilenceErrors: true,
		SilenceUsage:  true,
	}

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
			worker, _, err := newProcessor(ctx, cfg, logger)
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
			if stats.Failed > 0 {
				return fmt.Errorf("%d enrichment job(s) failed", stats.Failed)
			}
			return nil
		},
	}
	once.Flags().IntVar(&maxJobs, "max-jobs", 0, "maximum jobs to claim (default MAX_JOBS_PER_RUN)")
	root.AddCommand(once)

	var healthURL string
	var healthTimeout time.Duration
	healthcheck := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check a running service's liveness endpoint",
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
				return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
			}
			return nil
		},
	}
	healthcheck.Flags().StringVar(&healthURL, "url", "http://127.0.0.1:8080/healthz", "liveness endpoint URL")
	healthcheck.Flags().DurationVar(&healthTimeout, "timeout", 3*time.Second, "request timeout")
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
	worker, queue, err := newProcessor(ctx, cfg, logger)
	if err != nil {
		return err
	}
	tracker := health.NewTracker()
	management := dashboard.New(ctx, tracker, queue, worker, logger, cfg.MaxConcurrency)
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
		logger.Info("health server listening", "address", cfg.HTTPAddr)
		serverErrors <- server.ListenAndServe()
	}()
	go runScheduler(ctx, worker, tracker, cfg, logger)

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("health server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown health server: %w", err)
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
	run := func() {
		stats, err := worker.Run(ctx, cfg.MaxJobsPerRun)
		tracker.Record(stats, err)
		attributes := []any{
			"claimed", stats.Claimed,
			"completed", stats.Completed,
			"failed", stats.Failed,
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
			return
		case <-ticker.C:
			run()
		}
	}
}

func newProcessor(
	ctx context.Context,
	cfg config.Config,
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
	userAgent := "cairn-x-enricher/" + buildinfo.Version
	model := enrich.NewResponsesClient(
		cfg.GrokBaseURL,
		cfg.GrokAPIKey,
		cfg.GrokModel,
		cfg.GrokMaxTokens,
		userAgent,
		httpClient,
	)
	workflow, err := enrich.NewWorkflow(ctx, model)
	if err != nil {
		return nil, nil, err
	}
	return processor.New(queue, workflow, logger, cfg.MaxConcurrency), queue, nil
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
