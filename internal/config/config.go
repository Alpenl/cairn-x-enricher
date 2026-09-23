package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

const (
	defaultCairnBaseURL = "https://share.alpenl.com"
	defaultGrokModel    = "grok-4.6"
	defaultHTTPAddr     = ":8080"
)

// Config contains all validated settings needed by the service.
type Config struct {
	TypesafeBaseURL string
	TypesafeAPIKey  string
	TypesafeModel   string
	CairnBaseURL    string
	CairnToken      string
	GrokBaseURL     string
	GrokAPIKey      string
	GrokModel       string
	GrokMaxTokens   int
	PollInterval    time.Duration
	RequestTimeout  time.Duration

	// ShutdownTimeout bounds the total graceful shutdown: the HTTP server
	// drain and the in-flight work drain share this one budget, so it must
	// stay below the container's stop_grace_period. Docker sends SIGKILL
	// when that period elapses, which would cut the second phase short.
	ShutdownTimeout time.Duration

	MaxConcurrency int
	MaxJobsPerRun  int
	HTTPAddr       string
	LogLevel       string

	// The bounded semantic extensions are independently opt-in and default
	// off. A disabled extension is reported as unavailable rather than
	// silently succeeding, and it never blocks the ordinary pipeline.
	ExtensionEntities              bool
	ExtensionEvidence              bool
	ExtensionRerank                bool
	ExtensionProposal              bool
	ExtensionAllowlist             []string
	ExtensionMaxCalls              int
	ExtensionMaxCallsPerItem       int
	ExtensionMaxInputTokens        int
	ExtensionMaxInputTokensPerItem int
	ExtensionTimeout               time.Duration
	EntityCatalog                  extension.EntityCatalog

	// PartialReuse opts in to reusing unchanged stored answers for a new
	// classification. It is off by default; the conservative full evaluation is
	// the production default (R2-13).
	PartialReuse                        bool
	ClassificationMaxCalls              int
	ClassificationMaxCallsPerItem       int
	ClassificationMaxInputTokens        int
	ClassificationMaxInputTokensPerItem int
}

// Role identifies which components a command actually uses. Configuration is
// validated against the role so missing credentials for an unused component do
// not block a command that never calls it. Previously `classify` required the
// Grok settings even though it only talks to the Worker and Jev, so an
// operator could not run classification with a valid Jev key alone.
type Role string

const (
	// RoleServe runs the scheduler, which may use both reading and Jev.
	RoleServe Role = "serve"
	// RoleEnrich claims jobs that retrieve sources and generate reading aids.
	RoleEnrich Role = "enrich"
	// RoleClassify only processes stored sources with Jev.
	RoleClassify Role = "classify"
)

// Load reads and validates the full configuration. It is kept for callers that
// genuinely need every component and is equivalent to LoadFor(RoleServe).
func Load() (Config, error) { return LoadFor(RoleServe) }

// LoadFor reads the environment and validates only the components the role
// uses. Settings that are absent but unused are left empty rather than failing
// startup.
func LoadFor(role Role) (Config, error) {
	cfg := baseConfig()
	if err := cfg.readNumbers(); err != nil {
		return Config{}, err
	}
	if (role == RoleServe || role == RoleClassify) && cfg.TypesafeModel != "jev-1.13.0" {
		return Config{}, fmt.Errorf("classification budget requires pinned TYPESAFE_MODEL=jev-1.13.0 and matching controlled target")
	}
	if err := cfg.validateFor(role); err != nil {
		return Config{}, err
	}
	if (role == RoleServe || role == RoleClassify) && cfg.ExtensionEntities {
		var err error
		cfg.EntityCatalog, err = extension.LoadEntityCatalog(strings.TrimSpace(os.Getenv("CAIRN_ENTITY_CATALOG_PATH")))
		if err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// Load reads, applies defaults to, and validates runtime environment settings.
func baseConfig() Config {
	return Config{
		TypesafeBaseURL: valueOrDefault("TYPESAFE_BASE_URL", "https://api.typesafe.ai"),
		TypesafeAPIKey:  strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")),
		TypesafeModel:   valueOrDefault("TYPESAFE_MODEL", "jev-1.13.0"),
		CairnBaseURL:    valueOrDefault("CAIRN_API_BASE_URL", defaultCairnBaseURL),
		CairnToken:      strings.TrimSpace(os.Getenv("CAIRN_ENRICHER_TOKEN")),
		GrokBaseURL:     strings.TrimSpace(os.Getenv("GROK_MODELS_BASE_URL")),
		GrokAPIKey:      strings.TrimSpace(os.Getenv("XAI_API_KEY")),
		GrokModel:       valueOrDefault("GROK_MODEL", defaultGrokModel),
		HTTPAddr:        valueOrDefault("HTTP_ADDR", defaultHTTPAddr),
		LogLevel:        strings.ToLower(valueOrDefault("LOG_LEVEL", "info")),

		ExtensionEntities:  boolValue("CAIRN_EXTENSION_ENTITIES"),
		ExtensionEvidence:  boolValue("CAIRN_EXTENSION_EVIDENCE"),
		ExtensionRerank:    boolValue("CAIRN_EXTENSION_RERANK"),
		ExtensionProposal:  boolValue("CAIRN_EXTENSION_PROPOSAL"),
		ExtensionAllowlist: splitCSV(valueOrDefault("CAIRN_EVIDENCE_ALLOWED_HOSTS", "x.com,mp.weixin.qq.com")),
		PartialReuse:       boolValue("CAIRN_PARTIAL_REUSE"),
	}
}

// boolValue treats only an explicit truthy value as enabled; anything else,
// including a typo, leaves the extension off.
func boolValue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (c *Config) readNumbers() error {
	var err error
	if c.ClassificationMaxCalls, err = intValue("CAIRN_CLASSIFICATION_MAX_CALLS", 20, 1, 20); err != nil {
		return err
	}
	if c.ClassificationMaxCallsPerItem, err = intValue("CAIRN_CLASSIFICATION_MAX_CALLS_PER_ITEM", 5, 1, 5); err != nil {
		return err
	}
	if c.ClassificationMaxInputTokens, err = intValue("CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS", 20*65536, 1, 20*65536); err != nil {
		return err
	}
	if c.ClassificationMaxInputTokensPerItem, err = intValue("CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS_PER_ITEM", 5*65536, 1, 5*65536); err != nil {
		return err
	}
	if c.ExtensionMaxCalls, err = intValue("CAIRN_EXTENSION_MAX_CALLS", 20, 1, 20); err != nil {
		return err
	}
	if c.ExtensionMaxCallsPerItem, err = intValue("CAIRN_EXTENSION_MAX_CALLS_PER_ITEM", 2, 1, 2); err != nil {
		return err
	}
	if c.ExtensionMaxInputTokens, err = intValue("CAIRN_EXTENSION_MAX_INPUT_TOKENS", 20*65536, 1, 20*65536); err != nil {
		return err
	}
	if c.ExtensionMaxInputTokensPerItem, err = intValue("CAIRN_EXTENSION_MAX_INPUT_TOKENS_PER_ITEM", 2*65536, 1, 2*65536); err != nil {
		return err
	}
	if c.ExtensionTimeout, err = durationValue("CAIRN_EXTENSION_TIMEOUT", 20*time.Second); err != nil {
		return err
	}
	if c.ExtensionTimeout > 20*time.Second {
		return fmt.Errorf("CAIRN_EXTENSION_TIMEOUT must not exceed 20s")
	}
	if c.GrokMaxTokens, err = intValue("GROK_MAX_OUTPUT_TOKENS", 8192, 256, 32768); err != nil {
		return err
	}
	if c.PollInterval, err = durationValue("POLL_INTERVAL", 5*time.Minute); err != nil {
		return err
	}
	if c.RequestTimeout, err = durationValue("REQUEST_TIMEOUT", 3*time.Minute); err != nil {
		return err
	}
	if c.ShutdownTimeout, err = durationValue("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return err
	}
	if c.MaxConcurrency, err = intValue("MAX_CONCURRENCY", 2, 1, 16); err != nil {
		return err
	}
	if c.MaxJobsPerRun, err = intValue("MAX_JOBS_PER_RUN", 100, 1, 1000); err != nil {
		return err
	}
	return nil
}

// validateFor checks the components the role actually uses. The Worker token
// and HTTP/log settings are needed everywhere; Grok is only needed by the
// reading path; Typesafe is only needed by the classification path.
func (c Config) validateFor(role Role) error {
	if strings.TrimSpace(c.CairnToken) == "" {
		return fmt.Errorf("CAIRN_ENRICHER_TOKEN is required")
	}
	if err := validateBaseURL("CAIRN_API_BASE_URL", c.CairnBaseURL); err != nil {
		return err
	}

	needsReading := role == RoleServe || role == RoleEnrich
	needsClassification := role == RoleServe || role == RoleClassify

	if needsReading {
		for name, value := range map[string]string{
			"GROK_MODELS_BASE_URL": c.GrokBaseURL,
			"XAI_API_KEY":          c.GrokAPIKey,
			"GROK_MODEL":           c.GrokModel,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", name)
			}
		}
		if err := validateBaseURL("GROK_MODELS_BASE_URL", c.GrokBaseURL); err != nil {
			return err
		}
	}

	if needsClassification {
		for name, value := range map[string]string{
			"TYPESAFE_API_KEY": c.TypesafeAPIKey,
			"TYPESAFE_MODEL":   c.TypesafeModel,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", name)
			}
		}
		if err := validateBaseURL("TYPESAFE_BASE_URL", c.TypesafeBaseURL); err != nil {
			return err
		}
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
