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
	"sort"
	"strings"
)

// Provenance records where a gold label came from. A model output is never
// human gold, and a legacy implicit "reviewed" flag is not a positive sample.
type Provenance string

// Provenance values. A model output is never human gold.
const (
	ProvenanceHumanSingle   Provenance = "human_single"
	ProvenanceHumanReviewed Provenance = "human_reviewed"
	ProvenanceLegacyUnknown Provenance = "legacy_unknown"
	ProvenanceSynthetic     Provenance = "synthetic"
)

// Label is one gold value with an optional acceptable set. `Unknown` and
// `NotApplicable` are first-class so an annotator is never forced to invent a
// label for a dimension that does not apply.
type Label struct {
	// Values is the set of acceptable labels. A prediction matches if it is in
	// the set. An empty set with NotApplicable=true means "none applies".
	Values        []string `json:"values"`
	NotApplicable bool     `json:"not_applicable,omitempty"`
	Unknown       bool     `json:"unknown,omitempty"`
}

// Gold is the per-dimension human answer for one sample.
type Gold struct {
	Topics           Label `json:"topics"`
	ContentFunctions Label `json:"content_functions"`
	Carriers         Label `json:"carriers"`
	Affordances      Label `json:"affordances"`
	Form             Label `json:"form"`
	Use              Label `json:"use"`
}

// Sample is one evaluated record. It intentionally carries no private note or
// why text: the objective evaluation must not depend on personal fields, and a
// public report must not be able to reconstruct them.
type Sample struct {
	SampleID        string     `json:"sample_id"`
	SourceHash      string     `json:"source_hash"`
	Revision        int64      `json:"revision"`
	Language        string     `json:"language"`
	Carrier         string     `json:"carrier"`
	LengthBucket    string     `json:"length_bucket"`
	Completeness    string     `json:"completeness"`
	GroupID         string     `json:"group_id"`
	RetrievalQuery  string     `json:"retrieval_query,omitempty"`
	Relevance       []string   `json:"relevance,omitempty"`
	Gold            *Gold      `json:"gold,omitempty"`
	Provenance      Provenance `json:"provenance"`
	Ambiguous       bool       `json:"ambiguous,omitempty"`
	SecondAnnotator bool       `json:"second_annotator,omitempty"`
}

// Prediction is the system output for a sample, in the same dimensions.
type Prediction struct {
	SampleID         string   `json:"sample_id"`
	SpecID           string   `json:"spec_id"`
	SpecHash         string   `json:"spec_hash"`
	Model            string   `json:"model"`
	PolicyVersion    string   `json:"policy_version"`
	Topics           []string `json:"topics"`
	ContentFunctions []string `json:"content_functions"`
	Carriers         []string `json:"carriers"`
	Affordances      []string `json:"affordances"`
	Form             string   `json:"form"`
	Use              string   `json:"use"`
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

// Validate enforces the data contract. It rejects duplicates, missing gold and
// a split that leaks a group across sets, rather than silently scoring them.
func (d Dataset) Validate() error {
	if d.Name == "" {
		return errors.New("dataset name is required")
	}
	seen := map[string]bool{}
	groups := map[string]string{}
	for _, sample := range d.Samples {
		if sample.SampleID == "" {
			return errors.New("sample_id is required")
		}
		if seen[sample.SampleID] {
			return fmt.Errorf("duplicate sample_id %q", sample.SampleID)
		}
		seen[sample.SampleID] = true
		if sample.SourceHash == "" {
			return fmt.Errorf("sample %s is missing source_hash", sample.SampleID)
		}
		if sample.Provenance == "" {
			return fmt.Errorf("sample %s is missing provenance", sample.SampleID)
		}
		if sample.Provenance == ProvenanceLegacyUnknown && sample.Gold != nil {
			// A legacy implicit review must not be scored as human gold.
			return fmt.Errorf("sample %s is legacy_unknown and cannot carry gold", sample.SampleID)
		}
		if sample.GroupID != "" {
			if existing, ok := groups[sample.GroupID]; ok && existing != d.Split {
				return fmt.Errorf("group %s leaks across splits", sample.GroupID)
			}
			groups[sample.GroupID] = d.Split
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
		predicted[prediction.SampleID] = true
	}
	return nil
}

// SplitByGroup assigns whole groups to train/dev/test deterministically from a
// seed. Splitting by sample would leak a near-duplicate thread across sets.
func SplitByGroup(samples []Sample, seed int64, trainRatio, devRatio float64) (map[string][]Sample, error) {
	if trainRatio <= 0 || devRatio < 0 || trainRatio+devRatio >= 1 {
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
