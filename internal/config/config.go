package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCairnBaseURL = "https://share.alpenl.com"
	defaultGrokModel    = "grok-4.6"
	defaultHTTPAddr     = ":8080"
)

// Config contains all validated settings needed by the service.
type Config struct {
	CairnBaseURL   string
	CairnToken     string
	GrokBaseURL    string
	GrokAPIKey     string
	GrokModel      string
	GrokMaxTokens  int
	PollInterval   time.Duration
	RequestTimeout time.Duration

	// ShutdownTimeout bounds the total graceful shutdown: the HTTP server
	// drain and the in-flight work drain share this one budget, so it must
	// stay below the container's stop_grace_period. Docker sends SIGKILL
	// when that period elapses, which would cut the second phase short.
	ShutdownTimeout time.Duration

	MaxConcurrency int
	MaxJobsPerRun  int
	HTTPAddr       string
	LogLevel       string
}

// Load reads, applies defaults to, and validates runtime environment settings.
func Load() (Config, error) {
	cfg := Config{
		CairnBaseURL: valueOrDefault("CAIRN_API_BASE_URL", defaultCairnBaseURL),
		CairnToken:   strings.TrimSpace(os.Getenv("CAIRN_ENRICHER_TOKEN")),
		GrokBaseURL:  strings.TrimSpace(os.Getenv("GROK_MODELS_BASE_URL")),
		GrokAPIKey:   strings.TrimSpace(os.Getenv("XAI_API_KEY")),
		GrokModel:    valueOrDefault("GROK_MODEL", defaultGrokModel),
		HTTPAddr:     valueOrDefault("HTTP_ADDR", defaultHTTPAddr),
		LogLevel:     strings.ToLower(valueOrDefault("LOG_LEVEL", "info")),
	}

	var err error
	if cfg.GrokMaxTokens, err = intValue("GROK_MAX_OUTPUT_TOKENS", 8192, 256, 32768); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = durationValue("POLL_INTERVAL", 5*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = durationValue("REQUEST_TIMEOUT", 3*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationValue("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrency, err = intValue("MAX_CONCURRENCY", 2, 1, 16); err != nil {
		return Config{}, err
	}
	if cfg.MaxJobsPerRun, err = intValue("MAX_JOBS_PER_RUN", 100, 1, 1000); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate checks settings that are not validated by the numeric/duration parsers.
func (c Config) validate() error {
	for name, value := range map[string]string{
		"CAIRN_ENRICHER_TOKEN": c.CairnToken,
		"GROK_MODELS_BASE_URL": c.GrokBaseURL,
		"XAI_API_KEY":          c.GrokAPIKey,
		"GROK_MODEL":           c.GrokModel,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	if err := validateBaseURL("CAIRN_API_BASE_URL", c.CairnBaseURL); err != nil {
		return err
	}
	if err := validateBaseURL("GROK_MODELS_BASE_URL", c.GrokBaseURL); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error")
	}
	return nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationValue(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}

func intValue(name string, fallback, minValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
	}
	return value, nil
}

func validateBaseURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain credentials, query, or fragment", name)
	}
	return nil
}
