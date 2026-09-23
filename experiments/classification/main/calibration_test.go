package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func TestCalibrationCommandWritesPrivateDescriptiveReport(t *testing.T) {
	d, j := fittedJournalFixture(t)
	root := t.TempDir()
	journal := filepath.Join(root, "recovery.json")
	writeSingle(t, journal, j)
	output := filepath.Join(root, "diagnostics")
	config := "../reference-v1/calibration-v1.json"
	if err := runCalibrationCommand(d, config, journal, "../reference-v1/taxonomy.json", output); err != nil {
		t.Fatal(err)
	}
	var artifact evaluation.CalibrationArtifact
	readJSON(t, filepath.Join(output, "calibration.json"), &artifact)
	if artifact.ModelCalls != 0 || artifact.Promote || artifact.Calibrated || artifact.Samples != 1 || len(artifact.Dimensions) != 6 || artifact.OrdinalQualityEvaluated {
		t.Fatal("command fabricated validation or lost dimensions")
	}
	for _, name := range []string{"calibration.json", "inputs.json"} {
		info, err := os.Stat(filepath.Join(output, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("output artifact is not private", err)
		}
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("output directory is not private", err)
	}
	before, err := os.ReadFile(filepath.Clean(filepath.Join(output, "calibration.json")))
	if err != nil {
		t.Fatal(err)
	}
	if err := runCalibrationCommand(d, config, journal, "../reference-v1/taxonomy.json", output); err == nil {
		t.Fatal("existing report overwritten")
	}
	after, err := os.ReadFile(filepath.Clean(filepath.Join(output, "calibration.json")))
	if err != nil || string(before) != string(after) {
		t.Fatal("existing report changed")
	}
	j.ReferenceHash = "wrong"
	writeSingle(t, filepath.Join(root, "wrong.json"), j)
	if err := runCalibrationCommand(d, config, filepath.Join(root, "wrong.json"), "../reference-v1/taxonomy.json", filepath.Join(root, "rejected")); err == nil {
		t.Fatal("mismatched journal accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "rejected")); !os.IsNotExist(err) {
		t.Fatal("failed validation created output")
	}
}

func TestCalibrationRejectsHoldoutBeforeReadingFiles(t *testing.T) {
	if err := runCalibrationCommand(evaluation.Dataset{Split: "holdout"}, "missing", "missing", "missing", "missing"); err == nil || !strings.Contains(err.Error(), "never holdout") {
		t.Fatal(err)
	}
}
