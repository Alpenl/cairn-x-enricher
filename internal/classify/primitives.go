package classify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// Answer types. The three primitives are deliberately distinguishable types
// rather than one map because they carry different semantics and different
// validation rules:
//
//   - Noul is a probability that a statement holds. It is not a degree, an
//     importance weight or a confidence.
//   - Choice is a distribution over mutually exclusive options.
//   - Score is an ordinal rating with a distribution over levels.
//
// Collapsing them into one shape would let a caller compare a topic
// probability with an importance score, which the review explicitly forbids.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

// Probability is validated on every construction so an out-of-range value can
// never reach the policy layer.
func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// NoulAnswer is the typed transport form of a Noul judgment.
type NoulAnswer struct {
	Noul *float64 `json:"noul"`
}

// ChoiceAnswer is the typed transport form of a mutually exclusive Choice.
type ChoiceAnswer struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// ScoreAnswer is the typed transport form of an ordinal Score. Per the official
// TypeSafe contract, `score` is a probability-weighted float that may fall
// between levels, `legend` maps each level index back to its description and
// `probabilities` is keyed by the same level index as a string. The level order
// is the criteria order and must never be re-sorted: sorting would silently
// reinterpret the rubric.
type ScoreAnswer struct {
	Score         float64            `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// RawAnswer is one provider answer. Exactly one of the typed fields must be
// populated and it must match the question type; anything else is a contract
// error rather than a silently ignored field. Its JSON encoding matches the
// provider wire format exactly, so stored raw answers and decoded answers are
// inverses of each other (F06).
type RawAnswer struct {
	Type   string        `json:"type"`
	Noul   *NoulAnswer   `json:"noul,omitempty"`
	Choice *ChoiceAnswer `json:"choice,omitempty"`
	Score  *ScoreAnswer  `json:"score,omitempty"`
	// Confidence is retained as a distribution feature only. It is never
	// multiplied into the primitive probability.
	Confidence *float64 `json:"confidence,omitempty"`
}

// UnmarshalJSON decodes an answer from the provider wire format. The official
// shape inlines the primitive fields at the top level:
//
//	{"type":"noul","noul":0.93}
//	{"type":"choice","choice":"b","probabilities":{...},"confidence":0.8}
//	{"type":"score","score":1.05,"legend":{"0":"..."},"probabilities":{"0":...}}
func (a *RawAnswer) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type          string             `json:"type"`
		Noul          *float64           `json:"noul"`
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
		Score         *float64           `json:"score"`
		Legend        map[string]string  `json:"legend"`
		Confidence    *float64           `json:"confidence"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return err
	}
	a.Type = wire.Type
	a.Confidence = wire.Confidence
	switch wire.Type {
	case TypeNoul:
		if wire.Noul == nil {
			return errors.New("noul answer is missing its probability")
		}
		a.Noul = &NoulAnswer{Noul: wire.Noul}
	case TypeChoice:
		if wire.Choice == "" || wire.Probabilities == nil {
			return errors.New("choice answer is missing its option or distribution")
		}
		a.Choice = &ChoiceAnswer{Choice: wire.Choice, Probabilities: wire.Probabilities}
	case TypeScore:
		if wire.Score == nil || len(wire.Legend) == 0 || wire.Probabilities == nil {
			return errors.New("score answer is missing its value, legend or distribution")
		}
		a.Score = &ScoreAnswer{Score: *wire.Score, Legend: wire.Legend, Probabilities: wire.Probabilities}
	default:
		return fmt.Errorf("unknown answer type %q", wire.Type)
	}
	return nil
}

// MarshalJSON encodes the answer in the provider wire format. It is the exact
// inverse of UnmarshalJSON so a stored raw answer round-trips byte-for-byte
// through the canonical encoder (F06).
func (a RawAnswer) MarshalJSON() ([]byte, error) {
	payload := map[string]any{"type": a.Type}
	switch a.Type {
	case TypeNoul:
		if a.Noul == nil || a.Noul.Noul == nil {
			return nil, errors.New("noul answer is missing its probability")
		}
		payload["noul"] = *a.Noul.Noul
	case TypeChoice:
		if a.Choice == nil {
			return nil, errors.New("choice answer is missing its option")
		}
		payload["choice"] = a.Choice.Choice
		payload["probabilities"] = a.Choice.Probabilities
	case TypeScore:
		if a.Score == nil {
			return nil, errors.New("score answer is missing its value")
		}
		payload["score"] = a.Score.Score
		payload["legend"] = a.Score.Legend
		payload["probabilities"] = a.Score.Probabilities
	default:
		return nil, fmt.Errorf("unknown answer type %q", a.Type)
	}
	if a.Confidence != nil {
		payload["confidence"] = *a.Confidence
	}
	return json.Marshal(payload)
}

// DecodeAnswers decodes a JSON object of provider answers. It is the single
// decoder used for both wire responses and stored raw answers.
func DecodeAnswers(data []byte) (map[string]RawAnswer, error) {
	answers := map[string]RawAnswer{}
	if err := strictDecode(data, &answers); err != nil {
		return nil, err
	}
	return answers, nil
}

// ValidateProbabilityMap checks that the distribution is non-empty and covers
// the allowed keys exactly. For Choice the keys are option ids; for Score they
// are the level indices as strings. Probabilities must be in range and sum to
// one within a small tolerance.
func ValidateProbabilityMap(distribution map[string]float64, allowed []string) error {
	if len(distribution) != len(allowed) {
		return fmt.Errorf("distribution has %d options, want %d", len(distribution), len(allowed))
	}
	total := 0.0
	for _, option := range allowed {
		value, ok := distribution[option]
		if !ok {
			return fmt.Errorf("distribution is missing option %q", option)
		}
		if !validProbability(value) {
			return fmt.Errorf("distribution option %q is not a probability", option)
		}
		total += value
	}
	if math.Abs(total-1) > 0.01 {
		return fmt.Errorf("distribution sums to %.4f, want 1", total)
	}
	return nil
}

// DistributionFeature describes a distribution without reducing it to a single
// number. Two answers with the same maximum can differ in entropy, and that
// difference must survive into the raw record.
type DistributionFeature struct {
	Maximum  string
	MaxProb  float64
	Entropy  float64
	RunnerUp float64
	Margin   float64
}

// DescribeDistribution summarizes a probability map for audit. It does not
// decide anything; the policy does that from the full distribution.
func DescribeDistribution(distribution map[string]float64) DistributionFeature {
	options := make([]string, 0, len(distribution))
	for option := range distribution {
		options = append(options, option)
	}
	sort.Strings(options)
	feature := DistributionFeature{}
	for _, option := range options {
		value := distribution[option]
		if value > feature.MaxProb || (value == feature.MaxProb && option < feature.Maximum) {
			feature.Maximum = option
			feature.MaxProb = value
		}
		if value > 0 {
			feature.Entropy -= value * math.Log2(value)
		}
	}
	for _, option := range options {
		if option == feature.Maximum {
			continue
		}
		feature.RunnerUp = math.Max(feature.RunnerUp, distribution[option])
	}
	feature.Margin = feature.MaxProb - feature.RunnerUp
	return feature
}

// strictDecode decodes exactly one JSON value and rejects unknown fields and
// trailing data. It is the shared decoder for every model-controlled payload.
func strictDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

// rejectDuplicateKeys walks a JSON document and reports the first duplicate
// object key. encoding/json keeps the last value silently, which would let a
// provider bypass a schema constraint by repeating a key.
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkUniqueKeys(decoder); err != nil {
		return fmt.Errorf("invalid answer JSON: %w", err)
	}
	return nil
}

func walkUniqueKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = true
			if err := walkUniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkUniqueKeys(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return nil
	}
}
