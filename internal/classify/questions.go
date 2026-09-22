package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
// Instructions and Criteria. A dependent question may reference material that
// an independent question cannot see, so questions carry their own evidence
// selection rather than sharing one global blob.
//
// Instructions and Criteria hold the provider-facing structured values (a
// string, object or array) exactly as the official TypeSafe contract allows.
// Internal fields such as Dimension, TermID and DependsOn are never serialized
// to the provider.
type Question struct {
	ID           string          `json:"id"`
	Kind         QuestionKind    `json:"kind"`
	Instructions json.RawMessage `json:"instructions"`
	// Criteria is the provider-facing criteria value: an object of option
	// rubrics for Choice, an ordered array of level descriptions for Score, and
	// an optional yes/no clarification object for Noul.
	Criteria json.RawMessage `json:"criteria,omitempty"`
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
	Questions       []Question `json:"questions"`
	// ScoreEnabled is false until a product need and a calibrated scale exist.
	ScoreEnabled bool `json:"score_enabled"`
	// SemanticHash is the stable identity of the provider-visible question set
	// (type, instructions and criteria, plus the spec id and version). It is
	// computed by HashSpec and excluded from its own input, so a display label
	// rename that does not enter a question leaves it unchanged.
	SemanticHash string `json:"-"`
}

// scoreLevelDescriptions is the ordered criteria array for a Score question.
func (q Question) scoreLevelDescriptions() ([]string, error) {
	var levels []json.RawMessage
	if len(q.Criteria) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(q.Criteria, &levels); err != nil {
		return nil, fmt.Errorf("score question %s criteria must be an ordered array", q.ID)
	}
	descriptions := make([]string, 0, len(levels))
	for _, level := range levels {
		descriptions = append(descriptions, rawText(level))
	}
	return descriptions, nil
}

// rawText renders a structured criteria entry as a stable string. A string is
// returned as-is; objects and arrays are canonicalized so level identity is
// deterministic and order is never reinterpreted.
func rawText(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text
	}
	encoded, err := canonicalJSONBytes(json.RawMessage(value))
	if err != nil {
		return string(value)
	}
	return string(encoded)
}

// AnswerOptions returns the allowed answer keys for a question. A Noul answer
// has no distribution; Choice keys are option ids and Score keys are the level
// indices as strings, matching the official probabilities map.
func (q Question) AnswerOptions() []string {
	switch q.Kind {
	case QuestionChoice:
		var options map[string]json.RawMessage
		if err := json.Unmarshal(q.Criteria, &options); err != nil {
			return nil
		}
		keys := make([]string, 0, len(options))
		for option := range options {
			keys = append(keys, option)
		}
		sort.Strings(keys)
		return keys
	case QuestionScore:
		levels, err := q.scoreLevelDescriptions()
		if err != nil {
			return nil
		}
		indices := make([]string, len(levels))
		for index := range levels {
			indices[index] = fmt.Sprintf("%d", index)
		}
		return indices
	default:
		return nil
	}
}

// ScoreLegend returns the ordered level descriptions keyed by index string.
func (q Question) ScoreLegend() map[string]string {
	levels, err := q.scoreLevelDescriptions()
	if err != nil {
		return nil
	}
	legend := make(map[string]string, len(levels))
	for index, description := range levels {
		legend[fmt.Sprintf("%d", index)] = description
	}
	return legend
}

// CompileSpec builds the immutable question set for a catalog. The order is
// deterministic so the spec hash is stable across processes.
func CompileSpec(catalog taxonomy.Catalog, scoreEnabled bool) (QuestionSpec, error) {
	if err := catalog.Validate(); err != nil {
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
			Instructions: mustJSON(materialRule + "原帖是否实质讨论以下主题：" + semanticDescription(term) +
				"？不要仅根据收藏备注或偶然提及来打主题标签。"),
			Criteria: mustJSON(map[string]string{
				"true":  "主题是原帖主要讨论对象之一，有实质信息、论点、方法或案例。",
				"false": "未讨论此主题，或仅偶然提及、仅在评论出现。",
			}),
		})
	}
	// Method/tool/case/data/point and potential affordances can coexist, so
	// they are independent Noul judgments like topics.
	for _, dimension := range []struct {
		name, instruction string
		terms             []taxonomy.Term
	}{
		{"content_functions", "原帖是否包含以下内容功能（方法/工具/案例/数据/观点可并存）？", catalog.ContentFunctions},
		{"affordances", "这份材料是否具备以下潜在用途或参考价值？只是潜在可能，不代表用户确认的动机。", catalog.Affordances},
	} {
		for _, term := range dimension.terms {
			if !term.Active {
				continue
			}
			questions = append(questions, Question{
				ID: dimension.name + "_" + term.ID, Kind: QuestionNoul, Dimension: dimension.name, TermID: term.ID,
				Instructions: mustJSON(materialRule + dimension.instruction + semanticDescription(term) + "。"),
				Criteria: mustJSON(map[string]string{
					"true":  "原帖确实包含这一功能或价值。",
					"false": "未包含，或仅偶然提及。",
				}),
			})
		}
	}
	// Carrier describes the real structure of the source and is genuinely
	// mutually exclusive.
	if len(catalog.Carriers) > 0 {
		criteria := map[string]any{"none": "证据不足，无法判断真实结构。"}
		for _, term := range catalog.Carriers {
			if term.Active {
				criteria[term.ID] = semanticDescription(term)
				if term.ID == "unknown" {
					criteria["none"] = "结构可观察，但不属于词表中的任何载体，例如独立视频或书籍。不是证据不足；证据不足时选择 unknown。"
				}
			}
		}
		questions = append(questions, Question{
			ID: "carriers", Kind: QuestionChoice, Dimension: "carriers",
			Instructions: mustJSON(materialRule + "这份来源的真实结构是什么？只按可观察的结构判断，不按内容主题推断。"),
			Criteria:     mustJSON(criteria),
		})
	}
	// Form and use are genuinely mutually exclusive, so they are Choice.
	for _, dimension := range []struct {
		id, instruction string
		terms           []taxonomy.Term
	}{
		{"form", "原帖最适合归入哪一种内容形态？优先按实际内容功能判断；串推和长文只在其他形态都不贴切时选择。", catalog.Forms},
		{"use", "仅依据客观来源材料，这份内容最适合哪一种潜在用途？这是用途建议，不代表用户确认的收藏动机或立场；没有合适的客观用途时选择 none。", catalog.Uses},
	} {
		criteria := map[string]any{"none": "证据不足或没有合适选项。"}
		for _, term := range dimension.terms {
			if term.Active && (dimension.id != "use" || !taxonomy.PersonalUse(term.ID)) {
				criteria[term.ID] = semanticDescription(term)
			}
		}
		questions = append(questions, Question{
			ID: dimension.id, Kind: QuestionChoice, Dimension: dimension.id,
			Instructions: mustJSON(materialRule + dimension.instruction), Criteria: mustJSON(criteria),
		})
	}
	// Score is opt-in. When enabled, level order is explicit and meaningful.
	if scoreEnabled {
		questions = append(questions, Question{
			ID: "importance", Kind: QuestionScore, Dimension: "importance",
			Instructions: mustJSON(materialRule + "这份材料对读者的重要程度如何？等级从低到高：low、medium、high。"),
			Criteria:     mustJSON([]string{"low", "medium", "high"}),
		})
	}
	sort.SliceStable(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	// The spec id is content-addressed over the provider-visible semantics, so a
	// changed definition, criteria, boundary example or Score flag registers a
	// new immutable spec instead of colliding with the old one. A display-only
	// rename does not change it (R2-14).
	identity, err := hashSpecIdentity(QuestionSpec{SpecVersion: 1, Questions: questions, ScoreEnabled: scoreEnabled})
	if err != nil {
		return QuestionSpec{}, err
	}
	spec := QuestionSpec{
		SpecID: "classify-" + identity[:12], SpecVersion: 1, TaxonomyVersion: catalog.Version,
		Questions: questions, ScoreEnabled: scoreEnabled,
	}
	hash, err := HashSpec(spec)
	if err != nil {
		return QuestionSpec{}, err
	}
	spec.SemanticHash = hash
	return spec, nil
}

// hashSpecIdentity hashes the provider-visible semantics without the spec id, so
// the id can be derived from the content it identifies. It uses exactly the same
// projection as HashSpec minus the id.
func hashSpecIdentity(spec QuestionSpec) (string, error) {
	type hashQuestion struct {
		ID           string          `json:"id"`
		Kind         QuestionKind    `json:"kind"`
		Instructions json.RawMessage `json:"instructions"`
		Criteria     json.RawMessage `json:"criteria,omitempty"`
	}
	payload := struct {
		SpecVersion  int            `json:"spec_version"`
		ScoreEnabled bool           `json:"score_enabled"`
		Questions    []hashQuestion `json:"questions"`
	}{SpecVersion: spec.SpecVersion, ScoreEnabled: spec.ScoreEnabled}
	for _, question := range spec.Questions {
		payload.Questions = append(payload.Questions, hashQuestion{
			ID: question.ID, Kind: question.Kind, Instructions: question.Instructions, Criteria: question.Criteria,
		})
	}
	encoded, err := canonicalJSONBytes(payload)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// semanticDescription is the model-facing definition of a term. The display
// label is deliberately excluded: renaming a label for the UI must not change
// the request or invalidate a stored run (F14). The boundary examples are
// included: they change what the model is asked and therefore the spec identity
// (R2-14).
func semanticDescription(term taxonomy.Term) string {
	parts := []string{"定义：" + term.Description}
	if len(term.Aliases) > 0 {
		parts = append(parts, "别名："+strings.Join(term.Aliases, "、"))
	}
	if len(term.Includes) > 0 {
		parts = append(parts, "包含示例："+strings.Join(term.Includes, "、"))
	}
	if len(term.Excludes) > 0 {
		parts = append(parts, "排除示例："+strings.Join(term.Excludes, "、"))
	}
	return strings.Join(parts, "；")
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// HashSpec returns the stable semantic hash of the provider-visible question
// set. It covers the question type, instructions, criteria and the spec
// identity; it excludes internal handle fields and any display-only metadata
// that never enters a request.
func HashSpec(spec QuestionSpec) (string, error) {
	type hashQuestion struct {
		ID           string          `json:"id"`
		Kind         QuestionKind    `json:"kind"`
		Instructions json.RawMessage `json:"instructions"`
		Criteria     json.RawMessage `json:"criteria,omitempty"`
	}
	payload := struct {
		SpecID       string         `json:"spec_id"`
		SpecVersion  int            `json:"spec_version"`
		ScoreEnabled bool           `json:"score_enabled"`
		Questions    []hashQuestion `json:"questions"`
	}{SpecID: spec.SpecID, SpecVersion: spec.SpecVersion, ScoreEnabled: spec.ScoreEnabled}
	for _, question := range spec.Questions {
		payload.Questions = append(payload.Questions, hashQuestion{
			ID: question.ID, Kind: question.Kind, Instructions: question.Instructions, Criteria: question.Criteria,
		})
	}
	encoded, err := canonicalJSONBytes(payload)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// Validate enforces the shape each kind requires.
func (q Question) Validate() error {
	if strings.TrimSpace(q.ID) == "" || len(q.ID) > 64 {
		return errors.New("question ID must contain 1 to 64 bytes")
	}
	if err := validateStructured(q.Instructions, "instructions"); err != nil {
		return fmt.Errorf("question %s: %w", q.ID, err)
	}
	switch q.Kind {
	case QuestionNoul:
		var criteria map[string]json.RawMessage
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
			return fmt.Errorf("noul question %s criteria must be an object", q.ID)
		}
		if len(criteria["true"]) == 0 || len(criteria["false"]) == 0 {
			return fmt.Errorf("noul question %s must define true and false criteria", q.ID)
		}
	case QuestionChoice:
		var criteria map[string]json.RawMessage
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
			return fmt.Errorf("choice question %s criteria must be an option map", q.ID)
		}
		if len(criteria) < 2 {
			return fmt.Errorf("choice question %s must define at least two options", q.ID)
		}
	case QuestionScore:
		levels, err := q.scoreLevelDescriptions()
		if err != nil {
			return err
		}
		if len(levels) < 2 || len(levels) > 10 {
			return fmt.Errorf("score question %s must define 2 to 10 ordered levels", q.ID)
		}
		for _, level := range levels {
			if strings.TrimSpace(level) == "" {
				return fmt.Errorf("score question %s has an empty level", q.ID)
			}
		}
	default:
		return fmt.Errorf("question %s has unknown kind %q", q.ID, q.Kind)
	}
	return nil
}

// validateStructured accepts the official string | object | array forms.
func validateStructured(value json.RawMessage, field string) error {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return fmt.Errorf("%s must not be empty", field)
	}
	var generic any
	if err := json.Unmarshal(value, &generic); err != nil {
		return fmt.Errorf("%s is not valid JSON", field)
	}
	switch generic.(type) {
	case string, map[string]any, []any:
		return nil
	default:
		return fmt.Errorf("%s must be a string, object or array", field)
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
	if answer.Confidence != nil && !validProbability(*answer.Confidence) {
		return fmt.Errorf("confidence for %s is out of range", question.ID)
	}
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
		legend := question.ScoreLegend()
		if len(answer.Score.Legend) != len(legend) {
			return fmt.Errorf("score %s legend has %d levels, want %d", question.ID, len(answer.Score.Legend), len(legend))
		}
		for index, description := range legend {
			if answer.Score.Legend[index] != description {
				return fmt.Errorf("score %s legend does not match level %s", question.ID, index)
			}
		}
		indices := question.AnswerOptions()
		if err := ValidateProbabilityMap(answer.Score.Probabilities, indices); err != nil {
			return fmt.Errorf("score %s: %w", question.ID, err)
		}
		if math.IsNaN(answer.Score.Score) || math.IsInf(answer.Score.Score, 0) || answer.Score.Score < 0 || answer.Score.Score > float64(len(indices)-1) {
			return fmt.Errorf("score value for %s is outside its levels", question.ID)
		}
	}
	return nil
}

// DecodeSpec parses a stored spec payload. A payload that arrives as a quoted
// JSON string (an older API returned the TEXT column verbatim) is unwrapped so
// the historical storage contract stays replayable.
func DecodeSpec(payload []byte) (QuestionSpec, error) {
	trimmed := strings.TrimSpace(string(payload))
	if strings.HasPrefix(trimmed, `"`) {
		var unwrapped string
		if err := json.Unmarshal(payload, &unwrapped); err != nil {
			return QuestionSpec{}, fmt.Errorf("stored question spec is not decodable: %w", err)
		}
		payload = []byte(unwrapped)
	}
	var stored struct {
		SpecID          string     `json:"spec_id"`
		SpecVersion     int        `json:"spec_version"`
		TaxonomyVersion string     `json:"taxonomy_version"`
		Questions       []Question `json:"questions"`
		ScoreEnabled    bool       `json:"score_enabled"`
	}
	if err := strictDecode(payload, &stored); err != nil {
		return QuestionSpec{}, err
	}
	if stored.SpecID == "" || len(stored.Questions) == 0 {
		return QuestionSpec{}, errors.New("stored question spec is incomplete")
	}
	for _, question := range stored.Questions {
		if err := question.Validate(); err != nil {
			return QuestionSpec{}, err
		}
	}
	spec := QuestionSpec{SpecID: stored.SpecID, SpecVersion: stored.SpecVersion,
		TaxonomyVersion: stored.TaxonomyVersion, Questions: stored.Questions, ScoreEnabled: stored.ScoreEnabled}
	hash, err := HashSpec(spec)
	if err != nil {
		return QuestionSpec{}, err
	}
	spec.SemanticHash = hash
	return spec, nil
}

// MarshalSpec returns the canonical bytes used for the spec hash and the
// Worker's immutable spec registration. The SemanticHash itself is excluded.
func MarshalSpec(spec QuestionSpec) ([]byte, error) { return canonicalJSONBytes(spec) }

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
	case json.RawMessage:
		var generic any
		if err := json.Unmarshal(typed, &generic); err != nil {
			return nil, err
		}
		return normalizeJSON(generic)
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
