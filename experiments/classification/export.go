// Package classification contains the offline evaluation tools. This file is
// the B08 production export: it turns stored classification runs into a dataset
// the offline scorer can consume. It reads the Worker only, makes no model call
// and never fabricates a gold label: a machine prediction is explicitly not
// human gold, so the scorer reports it as inconclusive until a human labels it.
package classification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// PredictionSource is the narrow Worker surface the exporter needs.
type PredictionSource interface {
	GetRuns(context.Context, int64) ([]cairn.StoredRun, error)
	GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error)
	GetEvidence(context.Context, int64) (json.RawMessage, error)
}

// ExportOptions bounds one export. Link IDs are always explicit: there is no
// implicit full-library scan and no paid call.
type ExportOptions struct {
	LinkIDs []int64
	Name    string
	Split   string
	Now     time.Time
}

// ExportDataset builds a validated dataset from production runs. Each sample
// carries the run's real spec/model/policy identity and an empty gold, and the
// prediction is the deterministic decision over the stored answers — so a
// replay of the same run reproduces the same prediction without inference.
func ExportDataset(ctx context.Context, source PredictionSource, options ExportOptions) (Dataset, error) {
	if len(options.LinkIDs) == 0 {
		return Dataset{}, errors.New("an export requires explicit link ids")
	}
	if len(options.LinkIDs) > 500 {
		return Dataset{}, errors.New("an export is bounded to 500 links")
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	name := options.Name
	if name == "" {
		name = "production-export"
	}
	split := options.Split
	if split == "" {
		split = "holdout"
	}
	dataset := Dataset{Name: name, Split: split, Seed: now.Unix()}
	seen := map[int64]bool{}
	for _, id := range options.LinkIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		sample, prediction, err := exportLink(ctx, source, id)
		if err != nil {
			return Dataset{}, fmt.Errorf("link %d: %w", id, err)
		}
		if sample == nil {
			continue
		}
		dataset.Samples = append(dataset.Samples, *sample)
		dataset.Prediction = append(dataset.Prediction, *prediction)
	}
	if len(dataset.Samples) == 0 {
		return Dataset{}, errors.New("no replayable production run was found for the requested links")
	}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func exportLink(ctx context.Context, source PredictionSource, id int64) (*Sample, *Prediction, error) {
	runs, err := source.GetRuns(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	run, ok := newestCompleteRun(runs)
	if !ok {
		return nil, nil, nil
	}
	storedSpec, err := source.GetQuestionSpec(ctx, run.SpecID)
	if err != nil {
		return nil, nil, fmt.Errorf("load spec %s: %w", run.SpecID, err)
	}
	spec, err := classify.DecodeSpec(storedSpec.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("spec %s is not decodable: %w", run.SpecID, err)
	}
	raw, err := classify.DecodeStoredJudgments(spec, run.RequestedModel, run.ResolvedModel, run.Answers, run.Coverage)
	if err != nil {
		return nil, nil, fmt.Errorf("run %d is not replayable: %w", run.ID, err)
	}
	policy, err := classify.DecodePolicy(run.Policy)
	if err != nil {
		return nil, nil, fmt.Errorf("run %d has no historical policy: %w", run.ID, err)
	}
	proposals, err := classify.Decide(raw, policy)
	if err != nil {
		return nil, nil, fmt.Errorf("run %d cannot be re-decided: %w", run.ID, err)
	}
	contentHash := run.SpecHash
	if evidence, err := source.GetEvidence(ctx, id); err == nil {
		var payload struct {
			ContentHash string `json:"content_hash"`
		}
		if json.Unmarshal(evidence, &payload) == nil && payload.ContentHash != "" {
			contentHash = payload.ContentHash
		}
	}
	sample := Sample{
		SampleID:     fmt.Sprintf("link-%d-run-%d", id, run.ID),
		SourceHash:   contentHash,
		Revision:     run.ContentRevision,
		Language:     "",
		Carrier:      "",
		LengthBucket: "",
		Completeness: run.EvidenceCoverage,
		GroupID:      fmt.Sprintf("link-%d", id),
		// A production prediction is machine output, never human gold.
		Provenance: ProvenanceSynthetic,
	}
	prediction := Prediction{
		SampleID:           sample.SampleID,
		SpecID:             run.SpecID,
		SpecHash:           run.SpecHash,
		Model:              run.ResolvedModel,
		PolicyVersion:      run.PolicyVersion,
		Topics:             proposals.Topics,
		ContentFunctions:   proposals.ContentFunctions,
		Carriers:           proposals.Carriers,
		Affordances:        proposals.Affordances,
		Form:               proposals.Form,
		Use:                proposals.Use,
		TopicProbabilities: topicProbabilities(raw),
		Abstained:          abstainedDimensions(proposals),
	}
	return &sample, &prediction, nil
}

// newestCompleteRun picks the newest succeeded, complete run. A trailing
// partial/failed run is skipped rather than exported as if it were current.
func newestCompleteRun(runs []cairn.StoredRun) (cairn.StoredRun, bool) {
	for index := len(runs) - 1; index >= 0; index-- {
		run := runs[index]
		if run.Status == "succeeded" && run.Coverage == "complete" && len(run.Answers) > 0 {
			return run, true
		}
	}
	return cairn.StoredRun{}, false
}

func topicProbabilities(raw classify.RawJudgments) map[string]float64 {
	probabilities := map[string]float64{}
	for _, judgment := range raw.Judgments {
		if judgment.Kind != classify.QuestionNoul || judgment.Noul == nil {
			continue
		}
		id := judgment.TermID
		if id == "" {
			id = judgment.QuestionID
		}
		probabilities[id] = *judgment.Noul
	}
	if len(probabilities) == 0 {
		return nil
	}
	return probabilities
}

func abstainedDimensions(proposals classify.Proposals) []string {
	abstained := map[string]bool{}
	for _, decision := range proposals.Decisions {
		if decision.Verdict == classify.VerdictAbstained {
			abstained[decision.Dimension] = true
		}
	}
	out := make([]string, 0, len(abstained))
	for dimension := range abstained {
		out = append(out, dimension)
	}
	sort.Strings(out)
	return out
}

// MarshalDataset renders the dataset as stable JSON.
func MarshalDataset(dataset Dataset) ([]byte, error) {
	return json.MarshalIndent(dataset, "", "  ")
}
