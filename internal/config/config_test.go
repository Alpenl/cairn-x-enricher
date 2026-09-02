package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CairnBaseURL != defaultCairnBaseURL {
		t.Fatalf("CairnBaseURL = %q", cfg.CairnBaseURL)
	}
	if cfg.GrokModel != defaultGrokModel {
		t.Fatalf("GrokModel = %q", cfg.GrokModel)
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Fatalf("PollInterval = %s", cfg.PollInterval)
	}
	if cfg.MaxConcurrency != 2 || cfg.MaxJobsPerRun != 100 {
		t.Fatalf("unexpected processing defaults: %+v", cfg)
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("XAI_API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want required variable error")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "duration", key: "POLL_INTERVAL", value: "soon"},
		{name: "concurrency", key: "MAX_CONCURRENCY", value: "0"},
		{name: "tokens", key: "GROK_MAX_OUTPUT_TOKENS", value: "12"},
		{name: "base URL", key: "GROK_MODELS_BASE_URL", value: "file:///tmp/model"},
		{name: "listen address", key: "HTTP_ADDR", value: "8080"},
		{name: "log level", key: "LOG_LEVEL", value: "verbose"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"CAIRN_API_BASE_URL",
		"GROK_MODEL",
		"GROK_MAX_OUTPUT_TOKENS",
		"POLL_INTERVAL",
		"REQUEST_TIMEOUT",
		"SHUTDOWN_TIMEOUT",
		"MAX_CONCURRENCY",
		"MAX_JOBS_PER_RUN",
		"HTTP_ADDR",
		"LOG_LEVEL",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("CAIRN_ENRICHER_TOKEN", "test-enricher-token")
	t.Setenv("GROK_MODELS_BASE_URL", "https://models.example/v1")
	t.Setenv("XAI_API_KEY", "test-model-key")
}
