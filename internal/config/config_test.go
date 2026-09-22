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
		"CAIRN_EXTENSION_MAX_CALLS", "CAIRN_EXTENSION_MAX_CALLS_PER_ITEM",
		"CAIRN_EXTENSION_MAX_INPUT_TOKENS", "CAIRN_EXTENSION_MAX_INPUT_TOKENS_PER_ITEM", "CAIRN_EXTENSION_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("CAIRN_ENRICHER_TOKEN", "test-enricher-token")
	t.Setenv("GROK_MODELS_BASE_URL", "https://models.example/v1")
	t.Setenv("XAI_API_KEY", "test-model-key")
	t.Setenv("TYPESAFE_API_KEY", "test-typesafe-key")
	t.Setenv("TYPESAFE_BASE_URL", "")
	t.Setenv("TYPESAFE_MODEL", "")
}

func TestExtensionLimitsCanOnlyTightenTheDeploymentCeiling(t *testing.T) {
	for name, value := range map[string]string{
		"CAIRN_EXTENSION_MAX_CALLS": "21", "CAIRN_EXTENSION_MAX_CALLS_PER_ITEM": "3",
		"CAIRN_EXTENSION_MAX_INPUT_TOKENS": "1310721", "CAIRN_EXTENSION_MAX_INPUT_TOKENS_PER_ITEM": "131073", "CAIRN_EXTENSION_TIMEOUT": "21s",
	} {
		t.Run(name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(name, value)
			if _, err := Load(); err == nil {
				t.Fatal("widened ceiling accepted")
			}
		})
	}
	setRequiredEnv(t)
	t.Setenv("CAIRN_EXTENSION_MAX_CALLS", "4")
	t.Setenv("CAIRN_EXTENSION_MAX_CALLS_PER_ITEM", "1")
	t.Setenv("CAIRN_EXTENSION_MAX_INPUT_TOKENS", "65536")
	t.Setenv("CAIRN_EXTENSION_MAX_INPUT_TOKENS_PER_ITEM", "32768")
	t.Setenv("CAIRN_EXTENSION_TIMEOUT", "2s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExtensionMaxCalls != 4 || cfg.ExtensionMaxCallsPerItem != 1 || cfg.ExtensionMaxInputTokens != 65536 || cfg.ExtensionMaxInputTokensPerItem != 32768 || cfg.ExtensionTimeout != 2*time.Second {
		t.Fatal("configured limits not retained")
	}
}

// TestClassifyRoleDoesNotRequireGrok pins B02-T09: the classify command only
// talks to the Worker and Jev, so a deployment with a valid Jev key but no Grok
// credentials must still be able to run classification. Conversely, an enrich
// role must not silently start without the reading credentials it needs.
func TestClassifyRoleDoesNotRequireGrok(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("GROK_MODELS_BASE_URL", "")

	if _, err := LoadFor(RoleClassify); err != nil {
		t.Fatalf("LoadFor(RoleClassify) error = %v, want success without Grok config", err)
	}
	if _, err := LoadFor(RoleEnrich); err == nil {
		t.Fatal("LoadFor(RoleEnrich) error = nil, want missing Grok config error")
	}
}

func TestEnrichRoleDoesNotRequireTypesafe(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TYPESAFE_API_KEY", "")

	if _, err := LoadFor(RoleEnrich); err != nil {
		t.Fatalf("LoadFor(RoleEnrich) error = %v, want success without Typesafe config", err)
	}
	if _, err := LoadFor(RoleClassify); err == nil {
		t.Fatal("LoadFor(RoleClassify) error = nil, want missing Typesafe config error")
	}
}

func TestEveryRoleRequiresTheWorkerToken(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CAIRN_ENRICHER_TOKEN", "")
	for _, role := range []Role{RoleServe, RoleEnrich, RoleClassify} {
		if _, err := LoadFor(role); err == nil {
			t.Errorf("LoadFor(%s) error = nil, want missing Worker token error", role)
		}
	}
}

func TestClassificationBudgetConfiguration(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := LoadFor(RoleClassify)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TypesafeModel != "jev-1.13.0" || cfg.ClassificationMaxCalls != 20 || cfg.ClassificationMaxCallsPerItem != 5 || cfg.ClassificationMaxInputTokens != 20*65536 || cfg.ClassificationMaxInputTokensPerItem != 5*65536 {
		t.Fatalf("defaults %+v", cfg)
	}
	for _, pair := range [][2]string{{"CAIRN_CLASSIFICATION_MAX_CALLS", "21"}, {"CAIRN_CLASSIFICATION_MAX_CALLS_PER_ITEM", "6"}, {"CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS", "0"}, {"CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS_PER_ITEM", "327681"}, {"TYPESAFE_MODEL", "jev-latest"}} {
		t.Run(pair[0], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if _, err := LoadFor(RoleClassify); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	t.Setenv("CAIRN_CLASSIFICATION_MAX_CALLS", "1")
	t.Setenv("CAIRN_CLASSIFICATION_MAX_INPUT_TOKENS", "1")
	cfg, err = LoadFor(RoleClassify)
	if err != nil || cfg.ClassificationMaxCalls != 1 || cfg.ClassificationMaxInputTokens != 1 {
		t.Fatalf("tightening %v %+v", err, cfg)
	}
}
