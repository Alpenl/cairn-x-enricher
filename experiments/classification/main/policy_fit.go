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
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func attachRecoveredEvaluations(dataset evaluation.Dataset, journal recoveryResult) (evaluation.Dataset, error) {
	if len(dataset.Samples) == 0 {
		return evaluation.Dataset{}, errors.New("policy fit requires nonempty samples")
	}
	hash, err := evaluation.HashReference(dataset)
	if err != nil {
		return evaluation.Dataset{}, err
	}
	if journal.ReferenceHash != hash || journal.ModelCalls != 0 || journal.Recovered != len(dataset.Samples) || journal.Unattempted != 0 || len(journal.Calls) != len(dataset.Samples) {
		return evaluation.Dataset{}, errors.New("replay journal does not match the complete frozen dataset")
	}
	byID := map[string]recoveredCall{}
	for _, call := range journal.Calls {
		if _, exists := byID[call.SampleID]; exists || call.Raw == nil || call.RecoveryError != "" {
			return evaluation.Dataset{}, errors.New("missing, duplicate or failed journal evaluation")
		}
		byID[call.SampleID] = call
	}
	result := dataset
	result.Prediction = append([]evaluation.Prediction{}, dataset.Prediction...)
	if len(result.Prediction) != len(result.Samples) {
		return evaluation.Dataset{}, errors.New("one prediction per sample is required")
	}
	for i, p := range result.Prediction {
		call, ok := byID[p.SampleID]
		if !ok {
			return evaluation.Dataset{}, errors.New("prediction missing from replay journal")
		}
		result.Prediction[i].Evaluation = call.Raw
	}
	return result, nil
}

func runPolicyFitCommand(dataset evaluation.Dataset, configPath, journalPath, catalogPath, output string) error {
	if dataset.Split != "train" && dataset.Split != "dev" {
		return errors.New("policy fitting requires train or dev, never holdout")
	}
	if configPath == "" || journalPath == "" || catalogPath == "" || output == "" {
		return errors.New("policy fit requires config, replay journal, frozen catalog and a new output directory")
	}
	if err := dataset.Validate(); err != nil {
		return err
	}
	configRaw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return err
	}
	var config evaluation.PolicyFitConfig
	decoder := json.NewDecoder(bytes.NewReader(configRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("policy config must contain exactly one JSON value")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	journalRaw, err := os.ReadFile(filepath.Clean(journalPath))
	if err != nil {
		return err
	}
	var journal recoveryResult
	if err := json.Unmarshal(journalRaw, &journal); err != nil {
		return err
	}
	dataset, err = attachRecoveredEvaluations(dataset, journal)
	if err != nil {
		return err
	}
	catalogRaw, err := os.ReadFile(filepath.Clean(catalogPath))
	if err != nil {
		return err
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		return err
	}
	model := dataset.Prediction[0].Model
	verified, err := evaluation.PreparePolicyReplay(dataset, catalog, model)
	if err != nil {
		return err
	}
	artifact, err := evaluation.FitProductionPolicy(verified, config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("policy fit output must be new: %w", err)
	}
	if err := privateJSON(output, "policy-fit.json", artifact); err != nil {
		return err
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	if err := privateJSON(output, "inputs.json", map[string]string{"config_sha256": hash(configRaw), "journal_sha256": hash(journalRaw), "catalog_sha256": hash(catalogRaw), "dataset_hash": artifact.DatasetHash, "reference_hash": artifact.ReferenceHash}); err != nil {
		return err
	}
	if artifact.Selected != nil {
		candidate, err := evaluation.ReplayFittedPolicy(verified, artifact)
		if err != nil {
			return err
		}
		if err := privateJSON(output, "selected-dataset.json", candidate); err != nil {
			return err
		}
	}
	encode(map[string]any{"status": artifact.Status, "model_calls": 0, "promote": false, "candidates": len(artifact.Candidates), "samples": artifact.Samples, "independent_groups": artifact.IndependentGroups, "selected_policy": artifact.Selected, "reference_hash": artifact.ReferenceHash, "artifacts": output})
	return nil
}
