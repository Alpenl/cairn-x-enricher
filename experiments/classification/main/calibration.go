package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func runCalibrationCommand(dataset evaluation.Dataset, configPath, journalPath, catalogPath, output string) error {
	if dataset.Split != "train" && dataset.Split != "dev" {
		return errors.New("calibration diagnostics require train or dev, never holdout")
	}
	if configPath == "" || journalPath == "" || catalogPath == "" || output == "" {
		return errors.New("calibration requires config, replay journal, frozen catalog and new output directory")
	}
	configRaw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return err
	}
	var config evaluation.CalibrationConfig
	decoder := json.NewDecoder(bytes.NewReader(configRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("calibration config must contain exactly one JSON value")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	verified, journalRaw, catalogRaw, err := loadVerifiedReplay(dataset, journalPath, catalogPath)
	if err != nil {
		return err
	}
	artifact, err := verified.Calibration(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("calibration output must be new: %w", err)
	}
	if err := privateJSON(output, "calibration.json", artifact); err != nil {
		return err
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	if err := privateJSON(output, "inputs.json", map[string]string{"config_sha256": hash(configRaw), "journal_sha256": hash(journalRaw), "catalog_sha256": hash(catalogRaw), "dataset_hash": artifact.DatasetHash, "reference_hash": artifact.ReferenceHash}); err != nil {
		return err
	}
	encode(map[string]any{"status": "descriptive_uncalibrated", "model_calls": 0, "promote": false, "samples": artifact.Samples, "independent_groups": artifact.IndependentGroups, "reference_hash": artifact.ReferenceHash, "artifacts": output})
	return nil
}
