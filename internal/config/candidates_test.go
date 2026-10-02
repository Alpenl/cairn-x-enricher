package config

import "testing"

func TestCandidateRecallDefaultsOffAndPreservesPersistentBudget(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CAIRN_CLASSIFICATION_CANDIDATE_MAX_QUESTIONS", "")
	defaults, err := LoadFor(RoleClassify)
	if err != nil || defaults.ClassificationCandidateMaxQuestions != 0 {
		t.Fatalf("candidate mode must be explicit: %+v %v", defaults, err)
	}
	for _, value := range []string{"31", "32", "128"} {
		t.Setenv("CAIRN_CLASSIFICATION_CANDIDATE_MAX_QUESTIONS", value)
		cfg, err := LoadFor(RoleClassify)
		if err != nil || cfg.ClassificationCandidateMaxQuestions < 31 ||
			cfg.ClassificationMaxCalls != defaults.ClassificationMaxCalls || cfg.ClassificationMaxCallsPerItem != defaults.ClassificationMaxCallsPerItem ||
			cfg.ClassificationMaxInputTokens != defaults.ClassificationMaxInputTokens || cfg.ClassificationMaxInputTokensPerItem != defaults.ClassificationMaxInputTokensPerItem {
			t.Fatalf("candidate configuration relaxed admission budgets: %+v %v", cfg, err)
		}
	}
	for _, value := range []string{"-1", "129", "not-a-number"} {
		t.Setenv("CAIRN_CLASSIFICATION_CANDIDATE_MAX_QUESTIONS", value)
		if _, err := LoadFor(RoleClassify); err == nil {
			t.Fatal("invalid candidate configuration was accepted")
		}
	}
}
