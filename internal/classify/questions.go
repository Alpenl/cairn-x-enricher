package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// QuestionKind distinguishes the primitives a compiled question asks for.
type QuestionKind string

// Question kinds, one per primitive.
const (
	QuestionNoul   QuestionKind = QuestionKind(TypeNoul)
	QuestionChoice QuestionKind = QuestionKind(TypeChoice)
	QuestionScore  QuestionKind = QuestionKind(TypeScore)
)

// Question is a compiled, immutable question. Its ID is only a stable handle
// for joining answers back to questions; the full meaning lives in
// Instructions, Criteria and the ordered levels. A dependent question may
// reference material that an independent question cannot see, so questions
// carry their own evidence selection rather than sharing one global blob.
type Question struct {
	ID           string            `json:"id"`
	Kind         QuestionKind      `json:"kind"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
	Levels       []string          `json:"levels,omitempty"`
	// Dimension records which taxonomy dimension the question feeds, so the
	// policy can aggregate without parsing the question ID.
	Dimension string `json:"dimension"`
	TermID    string `json:"term_id,omitempty"`
	// DependsOn lists question IDs whose material must be present. A question
	// with dependencies is never batched with a state that lacks them.
	DependsOn []string `json:"depends_on,omitempty"`
}

// QuestionSpec is the compiled question set plus the identity of the taxonomy
// it was compiled from. Two catalogs with the same version but different term
// definitions must not share a spec.
type QuestionSpec struct {
	SpecID          string     `json:"spec_id"`
	SpecVersion     int        `json:"spec_version"`
	TaxonomyVersion string     `json:"taxonomy_version"`
	TaxonomyHash    string     `json:"taxonomy_hash"`
	Questions       []Question `json:"questions"`
	// ScoreEnabled is false until a product need and a calibrated scale exist.
	ScoreEnabled bool `json:"score_enabled"`
}

// CompileSpec builds the immutable question set for a catalog. The order is
// deterministic so the spec hash is stable across processes.
func CompileSpec(catalog taxonomy.Catalog, scoreEnabled bool) (QuestionSpec, error) {
	if err := catalog.Validate(); err != nil {
		return QuestionSpec{}, err
	}
	hash, err := HashCatalog(catalog)
	if err != nil {
		return QuestionSpec{}, err
	}
	questions := make([]Question, 0, len(catalog.Topics)+len(catalog.Forms)+len(catalog.Uses))
	// Topics are independent Noul judgments: whether a topic is substantively
	// discussed does not depend on the answer to another topic.
	for _, term := range catalog.Topics {
		if !term.Active {
			continue
		}
		questions = append(questions, Question{
			ID: "topic_" + term.ID, Kind: QuestionNoul, Dimension: "topic", TermID: term.ID,
			Instructions: materialRule + "原帖是否实质讨论以下主题：" + describe(term) +
				"？不要仅根据收藏备注或偶然提及来打主题标签。",
			Criteria: map[string]string{
				"true":  "主题是原帖主要讨论对象之一，有实质信息、论点、方法或案例。",
				"false": "未讨论此主题，或仅偶然提及、仅在评论出现。",
			},
		})
	}
	// Form and use are genuinely mutually exclusive, so they are Choice.
	for _, dimension := range []struct {
		id, instruction string
		terms           []taxonomy.Term
	}{
		{"form", "原帖最适合归入哪一种内容形态？优先按实际内容功能判断；串推和长文只在其他形态都不贴切时选择。", catalog.Forms},
		{"use", "依据原帖与明确的收藏备注，这份内容最适合哪一种潜在用途？这是用途建议，不代表用户确认的收藏动机；只有备注明确表达反对时才能选择反对。", catalog.Uses},
	} {
		criteria := map[string]string{"none": "证据不足或没有合适选项。"}
		for _, term := range dimension.terms {
			if term.Active {
				criteria[term.ID] = describe(term)
			}
		}
		questions = append(questions, Question{
			ID: dimension.id, Kind: QuestionChoice, Dimension: dimension.id,
			Instructions: materialRule + dimension.instruction, Criteria: criteria,
		})
	}
	// Score is opt-in. When enabled, level order is explicit and meaningful.
	if scoreEnabled {
		questions = append(questions, Question{
			ID: "importance", Kind: QuestionScore, Dimension: "importance",
			Instructions: materialRule + "这份材料对读者的重要程度如何？等级从低到高：low、medium、high。",
			Levels:       []string{"low", "medium", "high"},
		})
	}
	sort.SliceStable(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	return QuestionSpec{
		SpecID: "classify-v1", SpecVersion: 1, TaxonomyVersion: catalog.Version,
		TaxonomyHash: hash, Questions: questions, ScoreEnabled: scoreEnabled,
	}, nil
}

// HashCatalog returns a stable hash over the semantic content of the catalog,
// independent of key order and of terms that are inactive (they cannot be
// selected, so they cannot change an answer).
func HashCatalog(catalog taxonomy.Catalog) (string, error) {
	canonical := map[string]any{"version": catalog.Version}
	for name, terms := range map[string][]taxonomy.Term{"topics": catalog.Topics, "forms": catalog.Forms, "uses": catalog.Uses} {
		active := make([]map[string]any, 0, len(terms))
		for _, term := range terms {
			if !term.Active {
				continue
			}
			active = append(active, map[string]any{
				"id": term.ID, "label": term.Label, "description": term.Description,
				"aliases": sortedCopy(term.Aliases),
			})
		}
		sort.Slice(active, func(i, j int) bool { return active[i]["id"].(string) < active[j]["id"].(string) })
		canonical[name] = active
	}
	encoded, err := canonicalJSONBytes(canonical)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// Validate Questions enforces the shape each kind requires.
func (q Question) Validate() error {
	if strings.TrimSpace(q.ID) == "" || len(q.ID) > 64 {
		return errors.New("question ID must contain 1 to 64 bytes")
	}
	switch q.Kind {
	case QuestionNoul:
		if q.Criteria["true"] == "" || q.Criteria["false"] == "" {
			return fmt.Errorf("noul question %s must define true and false criteria", q.ID)
		}
	case QuestionChoice:
		if len(q.Criteria) < 2 {
			return fmt.Errorf("choice question %s must define at least two options", q.ID)
		}
	case QuestionScore:
		if len(q.Levels) < 2 {
			return fmt.Errorf("score question %s must define at least two ordered levels", q.ID)
		}
	}
	return nil
}

// AnswerOptions returns the allowed option set for a question. A Noul answer
// has no distribution; Choice and Score do.
func (q Question) AnswerOptions() []string {
	switch q.Kind {
	case QuestionChoice:
		options := make([]string, 0, len(q.Criteria))
		for option := range q.Criteria {
			options = append(options, option)
		}
		sort.Strings(options)
		return options
	case QuestionScore:
		return sortedCopy(q.Levels)
	default:
		return nil
	}
}

// ValidateAnswers checks a decoded answer set against the compiled questions.
// The set must match exactly: a missing, extra or mistyped answer is a contract
// error, never a silent empty result.
func ValidateAnswers(spec QuestionSpec, answers map[string]RawAnswer) error {
	if len(answers) != len(spec.Questions) {
		return fmt.Errorf("answer set has %d entries, want %d", len(answers), len(spec.Questions))
	}
	for _, question := range spec.Questions {
		answer, ok := answers[question.ID]
		if !ok {
			return fmt.Errorf("missing answer for %s", question.ID)
		}
		if err := validateAnswer(question, answer); err != nil {
			return err
		}
	}
	return nil
}

func validateAnswer(question Question, answer RawAnswer) error {
	switch question.Kind {
	case QuestionNoul:
		if answer.Type != TypeNoul || answer.Noul == nil || answer.Noul.Noul == nil {
			return fmt.Errorf("answer for %s is not a noul judgment", question.ID)
		}
		if !validProbability(*answer.Noul.Noul) {
			return fmt.Errorf("noul probability for %s is out of range", question.ID)
		}
	case QuestionChoice:
		if answer.Type != TypeChoice || answer.Choice == nil {
			return fmt.Errorf("answer for %s is not a choice", question.ID)
		}
		options := question.AnswerOptions()
		if !containsString(options, answer.Choice.Choice) {
			return fmt.Errorf("choice %q is not an option of %s", answer.Choice.Choice, question.ID)
		}
		if err := ValidateProbabilityMap(answer.Choice.Probabilities, options); err != nil {
			return fmt.Errorf("choice %s: %w", question.ID, err)
		}
	case QuestionScore:
		if answer.Type != TypeScore || answer.Score == nil {
			return fmt.Errorf("answer for %s is not a score", question.ID)
		}
		levels := question.AnswerOptions()
		if answer.Score.Score < 0 || answer.Score.Score >= len(levels) || levels[answer.Score.Score] != levelAt(answer.Score, levels) {
			return fmt.Errorf("score level for %s is out of range or misordered", question.ID)
		}
		if err := ValidateProbabilityMap(answer.Score.Probabilities, levels); err != nil {
			return fmt.Errorf("score %s: %w", question.ID, err)
		}
	}
	return nil
}

func levelAt(answer *ScoreAnswer, levels []string) string {
	if answer.Score < 0 || answer.Score >= len(levels) {
		return ""
	}
	return levels[answer.Score]
}

// DecodeSpec parses a stored spec payload.
func DecodeSpec(payload []byte) (QuestionSpec, error) {
	var spec QuestionSpec
	if err := strictDecode(payload, &spec); err != nil {
		return QuestionSpec{}, err
	}
	if spec.SpecID == "" || len(spec.Questions) == 0 {
		return QuestionSpec{}, errors.New("stored question spec is incomplete")
	}
	return spec, nil
}

// MarshalSpec returns the canonical bytes used for the spec hash.
func MarshalSpec(spec QuestionSpec) ([]byte, error) { return canonicalJSONBytes(spec) }

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// canonicalJSONBytes encodes JSON with sorted object keys so two structurally
// equal values hash identically. It mirrors the TypeScript canonicalJSON used
// by the Worker, and is checked against the same golden vectors.
func canonicalJSONBytes(value any) ([]byte, error) {
	normalized, err := normalizeJSON(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func normalizeJSON(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			normalized, err := normalizeJSON(entry)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			normalized, err := normalizeJSON(entry)
			if err != nil {
				return nil, err
			}
			out[index] = normalized
		}
		return out, nil
	default:
		// Re-encode structs and named types through JSON so struct tags and
		// omitempty are honoured, then normalise that form. Scalars and nil are
		// returned as-is; re-encoding them would recurse forever because they
		// normalise to themselves.
		switch value.(type) {
		case nil, bool, string,
			float64, float32, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, json.Number:
			return value, nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var generic any
		if err := json.Unmarshal(encoded, &generic); err != nil {
			return nil, err
		}
		return normalizeJSON(generic)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
