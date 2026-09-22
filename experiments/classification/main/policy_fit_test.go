package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func fittedJournalFixture(t *testing.T) (evaluation.Dataset, recoveryResult) {
	t.Helper()
	dataset, root := recoveryFixture(t)
	d, _, j, err := recoverWires(dataset, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	// Make an explicitly selected one-sample ENGINEERING corpus, retaining its
	// actual wire state. This is not a modification of any quality benchmark.
	d.Samples = d.Samples[:1]
	// The source fixture has an unknown affordance. This constructed command
	// test needs six known legal-empty references to exercise successful export;
	// it is explicitly synthetic and never written back to the frozen corpus.
	empty := evaluation.Label{NotApplicable: true}
	d.Samples[0].Gold = &evaluation.Gold{Topics: empty, ContentFunctions: empty, Carriers: empty, Affordances: empty, Form: empty, Use: empty}
	d.Samples[0].Provenance = evaluation.ProvenanceSynthetic
	d.Samples[0].Reference = nil
	j.Unattempted = 0
	j.ReferenceHash, err = evaluation.HashReference(d)
	if err != nil {
		t.Fatal(err)
	}
	return d, j
}

func TestPolicyFitCommandWritesPrivateReproducibleArtifact(t *testing.T) {
	d, j := fittedJournalFixture(t)
	root := t.TempDir()
	journal := filepath.Join(root, "recovery.json")
	writeSingle(t, journal, j)
	config := filepath.Join(root, "config.json")
	var c evaluation.PolicyFitConfig
	readJSON(t, "../reference-v1/policy-fit-v1.json", &c)
	c.Accept = []float64{0.8}
	c.Reject = []float64{0.2}
	c.Choice = []float64{0.65}
	writeSingle(t, config, c)
	output := filepath.Join(root, "fitted")
	if err := runPolicyFitCommand(d, config, journal, "../reference-v1/taxonomy.json", output); err != nil {
		t.Fatal(err)
	}
	var artifact evaluation.PolicyFitArtifact
	readJSON(t, filepath.Join(output, "policy-fit.json"), &artifact)
	if artifact.ModelCalls != 0 || artifact.Promote || artifact.Selected == nil || artifact.Selected.Calibrated {
		t.Fatal("offline fitting promoted or lost its result")
	}
	stat, err := os.Stat(output)
	if err != nil || stat.Mode().Perm() != 0o700 {
		t.Fatal("output is not private", err)
	}
	for _, name := range []string{"policy-fit.json", "inputs.json", "selected-dataset.json"} {
		p := filepath.Join(output, name)
		s, err := os.Stat(p)
		if err != nil || s.Mode().Perm() != 0o600 {
			t.Fatal("artifact is not private", err)
		}
	}
	before, err := os.ReadFile(filepath.Clean(filepath.Join(output, "policy-fit.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := runPolicyFitCommand(d, config, journal, "../reference-v1/taxonomy.json", output); err == nil {
		t.Fatal("existing artifact overwritten")
	}
	after, err := os.ReadFile(filepath.Clean(filepath.Join(output, "policy-fit.json")))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("retry changed existing artifact")
	}
}

func TestPolicyFitJournalCannotSubstituteMissingOrOtherSamples(t *testing.T) {
	d, j := fittedJournalFixture(t)
	encoded, _ := json.Marshal(j)
	for name, mutate := range map[string]func(*recoveryResult){
		"reference":      func(j *recoveryResult) { j.ReferenceHash = "different" },
		"missing":        func(j *recoveryResult) { j.Calls = nil },
		"duplicate":      func(j *recoveryResult) { j.Calls = append(j.Calls, j.Calls[0]) },
		"unknown sample": func(j *recoveryResult) { j.Calls[0].SampleID = "other" },
		"failed":         func(j *recoveryResult) { j.Calls[0].RecoveryError = "invalid" },
		"missing raw":    func(j *recoveryResult) { j.Calls[0].Raw = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var altered recoveryResult
			if err := json.Unmarshal(encoded, &altered); err != nil {
				t.Fatal(err)
			}
			mutate(&altered)
			if _, err := attachRecoveredEvaluations(d, altered); err == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}

func TestPolicyFitRejectsHoldoutBeforeReadingAnyReplayInputs(t *testing.T) {
	d := evaluation.Dataset{Split: "holdout"}
	if err := runPolicyFitCommand(d, "missing", "missing", "missing", "missing"); err == nil || !strings.Contains(err.Error(), "never holdout") {
		t.Fatal(err)
	}
}
