// Package evaluation provides the offline evaluation, calibration and
// promotion-gate tooling for the Jev classification policy.
//
// Dataset schemas and the read-only Worker exporter are shared with the CLI.
// Scoring, ablation and threshold search are deterministic and offline; they
// never run inside the serving path. Explicit experiment commands own evaluation.
package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// Provenance records where a gold label came from. A model output is never
// human gold, and a legacy implicit "reviewed" flag is not a positive sample.
type Provenance string

// Provenance values. A model output is never human gold.
const (
	ProvenanceHumanSingle        Provenance = "human_single"
	ProvenanceHumanReviewed      Provenance = "human_reviewed"
	ProvenanceLegacyUnknown      Provenance = "legacy_unknown"
	ProvenanceSynthetic          Provenance = "synthetic"
	ProvenanceMachinePrediction  Provenance = "machine_prediction"
	ProvenanceAutomaticReference Provenance = "automatic_reference"
)

// Label is one gold value with an optional acceptable set. `Unknown` and
// `NotApplicable` are first-class so an annotator is never forced to invent a
// label for a dimension that does not apply.
type Label struct {
	// Values is the required set for a multi-label dimension, or the set of
	// acceptable alternatives for a single choice. NotApplicable means none.
	Values        []string `json:"values"`
	NotApplicable bool     `json:"not_applicable,omitempty"`
	Unknown       bool     `json:"unknown,omitempty"`
}

// Gold is the reference answer, whose origin is explicitly recorded in Sample.
// Nil Values without NotApplicable means unspecified, not a negative label.
type Gold struct {
	Topics           Label `json:"topics"`
	ContentFunctions Label `json:"content_functions"`
	Carriers         Label `json:"carriers"`
	Affordances      Label `json:"affordances"`
	Form             Label `json:"form"`
	Use              Label `json:"use"`
}

// ReferenceMetadata makes constructed/automated reference labels auditable.
// It must describe a basis fixed before observing the evaluated predictions.
type ReferenceMetadata struct {
	Method  string `json:"method"`
	Version string `json:"version"`
	Basis   string `json:"basis"`
	Source  string `json:"source"`
}

// Sample is one evaluated record. It intentionally carries no private note or
// why text: the objective evaluation must not depend on personal fields, and a
// public report must not be able to reconstruct them.
type Sample struct {
	SourceHashUnknown bool               `json:"source_hash_unknown,omitempty"`
	SampleID          string             `json:"sample_id"`
	SourceHash        string             `json:"source_hash"`
	Revision          int64              `json:"revision"`
	Language          string             `json:"language"`
	Carrier           string             `json:"carrier"`
	LengthBucket      string             `json:"length_bucket"`
	Completeness      string             `json:"completeness"`
	GroupID           string             `json:"group_id"`
	RetrievalQuery    string             `json:"retrieval_query,omitempty"`
	Relevance         []string           `json:"relevance,omitempty"`
	Gold              *Gold              `json:"gold,omitempty"`
	Provenance        Provenance         `json:"provenance"`
	Ambiguous         bool               `json:"ambiguous,omitempty"`
	SecondAnnotator   bool               `json:"second_annotator,omitempty"`
	Reference         *ReferenceMetadata `json:"reference,omitempty"`
	Material          *classify.Evidence `json:"material,omitempty"`
}

// Prediction is the system output for a sample, in the same dimensions.
type Prediction struct {
	Evaluation         *classify.RawJudgments `json:"evaluation,omitempty"`
	EvidenceSnapshotID int64                  `json:"evidence_snapshot_id,omitempty"`
	SampleID           string                 `json:"sample_id"`
	SpecID             string                 `json:"spec_id"`
	SpecHash           string                 `json:"spec_hash"`
	Model              string                 `json:"model"`
	PolicyVersion      string                 `json:"policy_version"`
	Topics             []string               `json:"topics"`
	ContentFunctions   []string               `json:"content_functions"`
	Carriers           []string               `json:"carriers"`
	Affordances        []string               `json:"affordances"`
	Form               string                 `json:"form"`
	Use                string                 `json:"use"`
	// TopicProbabilities retains the distribution so a calibration metric can
	// be computed without re-running the model.
	TopicProbabilities map[string]float64 `json:"topic_probabilities,omitempty"`
	// Abstained records fields the policy left undecided. An abstention is not
	// an error, but a policy that abstains on everything cannot look precise.
	Abstained []string `json:"abstained,omitempty"`
}

// Dataset is a validated set of samples and predictions.
type Dataset struct {
	Name       string       `json:"name"`
	Split      string       `json:"split"`
	Seed       int64        `json:"seed"`
	Samples    []Sample     `json:"samples"`
	Prediction []Prediction `json:"predictions"`
}

// Validate checks one dataset. Missing reference labels are allowed and reported
// as inconclusive; ValidateSplits checks leakage across multiple datasets.
func (d Dataset) Validate() error {
	if d.Name == "" {
		return errors.New("dataset name is required")
	}
	seen := map[string]bool{}

	for _, sample := range d.Samples {
		if sample.SampleID == "" {
			return errors.New("sample_id is required")
		}
		if seen[sample.SampleID] {
			return fmt.Errorf("duplicate sample_id %q", sample.SampleID)
		}
		seen[sample.SampleID] = true
		if sample.SourceHashUnknown && (sample.SourceHash != "" || sample.Material != nil || sample.Gold != nil) {
			return fmt.Errorf("sample %s claims both unknown and known source identity", sample.SampleID)
		}
		if sample.SourceHash == "" && !sample.SourceHashUnknown {
			return fmt.Errorf("sample %s is missing source_hash", sample.SampleID)
		}
		switch sample.Provenance {
		case ProvenanceHumanSingle, ProvenanceHumanReviewed, ProvenanceLegacyUnknown, ProvenanceSynthetic, ProvenanceAutomaticReference, ProvenanceMachinePrediction:
		default:
			return fmt.Errorf("sample %s has invalid provenance", sample.SampleID)
		}
		if (sample.Provenance == ProvenanceLegacyUnknown || sample.Provenance == ProvenanceMachinePrediction) && sample.Gold != nil {
			return fmt.Errorf("sample %s is legacy_unknown and cannot carry gold", sample.SampleID)
		}
		if sample.Provenance == ProvenanceAutomaticReference {
			if sample.Gold == nil || sample.Reference == nil || sample.Reference.Method == "" || sample.Reference.Version == "" || sample.Reference.Source == "" || sample.Reference.Basis == "" {
				return fmt.Errorf("sample %s lacks automatic reference provenance", sample.SampleID)
			}
			if sample.SecondAnnotator {
				return fmt.Errorf("sample %s cannot claim a second human annotator for automatic references", sample.SampleID)
			}
		}
		if sample.Gold != nil {
			for name, label := range referenceLabels(*sample.Gold) {
				if (label.Unknown && label.NotApplicable) || ((label.Unknown || label.NotApplicable) && len(label.Values) > 0) {
					return fmt.Errorf("sample %s has contradictory %s reference", sample.SampleID, name)
				}
				if err := uniqueValues(label.Values); err != nil {
					return fmt.Errorf("sample %s %s: %w", sample.SampleID, name, err)
				}
			}
		}
		if sample.Material != nil {
			hash, err := HashMaterial(*sample.Material)
			if err != nil {
				return err
			}
			if hash != sample.SourceHash {
				return fmt.Errorf("sample %s material hash mismatch", sample.SampleID)
			}
		}
	}
	predicted := map[string]bool{}
	for _, prediction := range d.Prediction {
		if !seen[prediction.SampleID] {
			return fmt.Errorf("prediction for unknown sample %s", prediction.SampleID)
		}
		if predicted[prediction.SampleID] {
			return fmt.Errorf("duplicate prediction for %s", prediction.SampleID)
		}
		for _, values := range [][]string{prediction.Topics, prediction.ContentFunctions, prediction.Carriers, prediction.Affordances, prediction.Abstained} {
			if err := uniqueValues(values); err != nil {
				return fmt.Errorf("prediction %s: %w", prediction.SampleID, err)
			}
		}
		if len(prediction.Carriers) > 1 {
			return fmt.Errorf("prediction %s has multiple carriers", prediction.SampleID)
		}
		for label, probability := range prediction.TopicProbabilities {
			if label == "" || math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
				return fmt.Errorf("prediction %s has invalid probability", prediction.SampleID)
			}
		}
		predicted[prediction.SampleID] = true
	}
	return nil
}

// SplitByGroup assigns whole groups to train/dev/test deterministically from a
// seed. Splitting by sample would leak a near-duplicate thread across sets.
func SplitByGroup(samples []Sample, seed int64, trainRatio, devRatio float64) (map[string][]Sample, error) {
	if math.IsNaN(trainRatio) || math.IsNaN(devRatio) || trainRatio <= 0 || devRatio < 0 || trainRatio+devRatio >= 1 {
		return nil, errors.New("split ratios must leave a non-empty test set")
	}
	groups := map[string][]Sample{}
	for _, sample := range samples {
		key := sample.GroupID
		if key == "" {
			key = sample.SampleID
		}
		groups[key] = append(groups[key], sample)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string][]Sample{"train": {}, "dev": {}, "test": {}}
	for _, key := range keys {
		// A stable hash-based assignment keeps the split reproducible without
		// depending on map iteration order.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, key)))
		fraction := float64(binaryFraction(sum[:])) / float64(1<<32)
		switch {
		case fraction < trainRatio:
			out["train"] = append(out["train"], groups[key]...)
		case fraction < trainRatio+devRatio:
			out["dev"] = append(out["dev"], groups[key]...)
		default:
			out["test"] = append(out["test"], groups[key]...)
		}
	}
	for _, split := range out {
		sort.Slice(split, func(i, j int) bool { return split[i].SampleID < split[j].SampleID })
	}
	return out, nil
}

func binaryFraction(data []byte) uint32 {
	var value uint32
	for index := 0; index < 4 && index < len(data); index++ {
		value = value<<8 | uint32(data[index])
	}
	return value
}

// HashDataset returns a stable identity for a dataset so a report can cite the
// exact split it scored.
func HashDataset(d Dataset) (string, error) {
	encoded, err := json.Marshal(struct {
		Name       string       `json:"name"`
		Split      string       `json:"split"`
		Seed       int64        `json:"seed"`
		Samples    []Sample     `json:"samples"`
		Prediction []Prediction `json:"predictions"`
	}{d.Name, d.Split, d.Seed, d.Samples, d.Prediction})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeLanguage buckets a language tag so a report groups the intended
// strata without depending on exact BCP-47 spelling.
func NormalizeLanguage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "zh", "zh-cn", "zh-hans", "chinese", "中文":
		return "zh"
	case "en", "en-us", "english":
		return "en"
	case "":
		return "unknown"
	default:
		return "mixed"
	}
}

func uniqueValues(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			return errors.New("empty or duplicate value")
		}
		seen[value] = true
	}
	return nil
}

func referenceLabels(g Gold) map[string]Label {
	return map[string]Label{"topics": g.Topics, "content_functions": g.ContentFunctions, "carriers": g.Carriers, "affordances": g.Affordances, "form": g.Form, "use": g.Use}
}

func knownLabel(label Label) bool {
	return !label.Unknown && (label.Values != nil || label.NotApplicable)
}

// HashMaterial identifies the complete objective evidence, before provider budgets.
func HashMaterial(material classify.Evidence) (string, error) {
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// HashReference excludes predictions, so an evaluation can cite the reference
// that was frozen before any model call.
func HashReference(d Dataset) (string, error) { d.Prediction = nil; return HashDataset(d) }

// ValidateSplits checks the whole frozen corpus, including source duplicates
// disguised by different sample/group IDs. Each source snapshot occurs once.
func ValidateSplits(datasets []Dataset) error {
	splits, ids, sources, groups := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, dataset := range datasets {
		if dataset.Split != "train" && dataset.Split != "dev" && dataset.Split != "holdout" && dataset.Split != "test" {
			return errors.New("invalid split")
		}
		if splits[dataset.Split] {
			return fmt.Errorf("duplicate split %s", dataset.Split)
		}
		splits[dataset.Split] = true
		if err := dataset.Validate(); err != nil {
			return err
		}
		for _, sample := range dataset.Samples {
			if ids[sample.SampleID] {
				return fmt.Errorf("duplicate sample %s across splits", sample.SampleID)
			}
			ids[sample.SampleID] = true
			if sample.SourceHashUnknown || sample.SourceHash == "" {
				return fmt.Errorf("sample %s lacks source identity for leakage checks", sample.SampleID)
			}
			if sources[sample.SourceHash] {
				return fmt.Errorf("duplicate source snapshot %s", sample.SourceHash)
			}
			sources[sample.SourceHash] = true
			if sample.GroupID == "" {
				return fmt.Errorf("sample %s lacks a group", sample.SampleID)
			}
			if prior, ok := groups[sample.GroupID]; ok && prior != dataset.Split {
				return fmt.Errorf("group %s leaks across splits", sample.GroupID)
			}
			groups[sample.GroupID] = dataset.Split
		}
	}
	return nil
}
