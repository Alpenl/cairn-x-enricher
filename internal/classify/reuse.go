package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// QuestionHash is the semantic identity of one compiled question. It covers the
// type, instructions and criteria — everything that changes the model input —
// and never the internal handle fields or a display label (F14/SC26).
func QuestionHash(question Question) (string, error) {
	payload := struct {
		ID           string          `json:"id"`
		Kind         QuestionKind    `json:"kind"`
		Instructions json.RawMessage `json:"instructions"`
		Criteria     json.RawMessage `json:"criteria,omitempty"`
	}{ID: question.ID, Kind: question.Kind, Instructions: question.Instructions, Criteria: question.Criteria}
	encoded, err := canonicalJSONBytes(payload)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// QuestionCacheKey identifies one reusable question answer. Reuse is only legal
// when the evidence, the exact question semantics, the resolved model and the
// batch semantics all match; a same-ID question with a different definition must
// never be reused.
func QuestionCacheKey(evidenceHash, questionHash, model, batchSemantics string) string {
	return evidenceHash + "|" + questionHash + "|" + model + "|" + batchSemantics
}

// ReuseDecision is the per-question outcome of a partial re-evaluation plan.
type ReuseDecision struct {
	QuestionID string `json:"question_id"`
	Reusable   bool   `json:"reusable"`
	Reason     string `json:"reason"`
	CacheKey   string `json:"cache_key,omitempty"`
}

// ReusePlan is the conservative plan for reusing stored answers. It never claims
// completeness: [ToInfer] is what a provider call must still answer, and a
// question with no stored answer is always re-inferred.
type ReusePlan struct {
	Decisions []ReuseDecision `json:"decisions"`
	ToInfer   []string        `json:"to_infer"`
	Reusable  []string        `json:"reusable"`
}

// PlanReuse decides, per question, whether a stored judgment may be reused for
// the given evidence, model and batch semantics. It is a pure function so the
// decision is reproducible and auditable.
func PlanReuse(previous *RawJudgments, next QuestionSpec, evidenceHash, model, batchSemantics string) (ReusePlan, error) {
	plan := ReusePlan{Decisions: []ReuseDecision{}, ToInfer: []string{}, Reusable: []string{}}
	for _, question := range next.Questions {
		hash, err := QuestionHash(question)
		if err != nil {
			return ReusePlan{}, fmt.Errorf("hash question %s: %w", question.ID, err)
		}
		decision := ReuseDecision{QuestionID: question.ID}
		switch {
		case previous == nil:
			decision.Reason = "no previous run"
		case len(previous.Judgments) == 0:
			decision.Reason = "previous run has no judgments"
		case previous.EvidenceHash == "":
			decision.Reason = "previous run has no evidence hash"
		case previous.EvidenceHash != evidenceHash:
			decision.Reason = "evidence changed"
		case previous.ResolvedModel != model:
			decision.Reason = "model changed"
		default:
			storedHash, ok := previous.QuestionHashes[question.ID]
			if !ok {
				decision.Reason = "question is new or its definition was not recorded"
				break
			}
			if storedHash != hash {
				decision.Reason = "question definition changed"
				break
			}
			if _, ok := previous.Judgments[question.ID]; !ok {
				decision.Reason = "previous run did not answer this question"
				break
			}
			decision.Reusable = true
			decision.Reason = "same evidence, question, model and batch semantics"
			decision.CacheKey = QuestionCacheKey(evidenceHash, hash, model, batchSemantics)
		}
		plan.Decisions = append(plan.Decisions, decision)
		if decision.Reusable {
			plan.Reusable = append(plan.Reusable, question.ID)
		} else {
			plan.ToInfer = append(plan.ToInfer, question.ID)
		}
	}
	sort.Strings(plan.Reusable)
	sort.Strings(plan.ToInfer)
	return plan, nil
}

// ErrReuseUnsafe reports a merge that would mix results that are not
// comparable. It is returned instead of producing a plausible-looking run.
var ErrReuseUnsafe = errors.New("partial reuse would mix incomparable results")

// EvaluateReusing performs the smallest provider call the plan allows and merges
// the stored answers with the new ones. Coverage is explicit: a question the
// provider did not answer makes the merged run "partial", never "complete"
// (SC26).
func (c *Client) EvaluateReusing(ctx context.Context, input Input, previous *RawJudgments, evidenceHash, batchSemantics string) (RawJudgments, error) {
	// Answers from another resolved model are not comparable, so a partial
	// re-evaluation refuses to merge them instead of producing a mixed run.
	if previous != nil && previous.ResolvedModel != "" && previous.ResolvedModel != c.model {
		return RawJudgments{}, fmt.Errorf("%w: previous run resolved to %s", ErrReuseUnsafe, previous.ResolvedModel)
	}
	plan, err := PlanReuse(previous, c.spec, evidenceHash, c.model, batchSemantics)
	if err != nil {
		return RawJudgments{}, err
	}
	if len(plan.ToInfer) == 0 {
		// Nothing changed: reuse every answer with zero model calls.
		merged := RawJudgments{
			SpecID: c.spec.SpecID, SpecHash: c.spec.SemanticHash, TaxonomyVersion: c.spec.TaxonomyVersion,
			RequestedModel: c.model, ResolvedModel: c.model,
			Judgments: map[string]RawJudgment{}, Coverage: "complete",
			EvidenceHash: evidenceHash, QuestionHashes: map[string]string{},
			Reused: plan.Reusable, BatchSemantics: batchSemantics,
		}
		for _, question := range c.spec.Questions {
			judgment, ok := previous.Judgments[question.ID]
			if !ok {
				return RawJudgments{}, fmt.Errorf("%w: missing stored answer for %s", ErrReuseUnsafe, question.ID)
			}
			merged.Judgments[question.ID] = judgment
			hash, _ := QuestionHash(question)
			merged.QuestionHashes[question.ID] = hash
		}
		return merged, nil
	}
	// Ask only the questions that actually changed.
	subset := make([]Question, 0, len(plan.ToInfer))
	for _, question := range c.spec.Questions {
		for _, id := range plan.ToInfer {
			if question.ID == id {
				subset = append(subset, question)
			}
		}
	}
	fresh, err := c.evaluateQuestions(ctx, input, subset, evidenceHash, batchSemantics)
	if err != nil {
		return RawJudgments{}, err
	}
	merged := fresh
	merged.Reused = plan.Reusable
	merged.BatchSemantics = batchSemantics
	if previous != nil {
		for _, id := range plan.Reusable {
			stored, ok := previous.Judgments[id]
			if !ok {
				return RawJudgments{}, fmt.Errorf("%w: missing stored answer for %s", ErrReuseUnsafe, id)
			}
			merged.Judgments[id] = stored
			merged.QuestionHashes[id] = previous.QuestionHashes[id]
		}
	}
	// Coverage is honest: every question must have an answer for "complete".
	if len(merged.Judgments) != len(c.spec.Questions) {
		merged.Coverage = "partial"
	}
	return merged, nil
}

// evaluateQuestions runs one bounded provider request for a question subset and
// returns the replayable judgments for exactly those questions.
func (c *Client) evaluateQuestions(ctx context.Context, input Input, questions []Question, evidenceHash, batchSemantics string) (RawJudgments, error) {
	if len(questions) == 0 {
		return RawJudgments{}, errors.New("no questions to evaluate")
	}
	if strings.TrimSpace(input.OriginalText) == "" {
		return RawJudgments{}, errors.New("classification requires source text")
	}
	evidence, err := PrepareEvidence(input.OriginalText, contextBlocks(input.ContextText), c.budget)
	if err != nil {
		return RawJudgments{}, err
	}
	state, err := evidence.stateForModel()
	if err != nil {
		return RawJudgments{}, err
	}
	wire := make(map[string]providerQuestion, len(questions))
	for _, question := range questions {
		wire[question.ID] = providerQuestion{
			Type: string(question.Kind), Instructions: question.Instructions, Criteria: question.Criteria,
		}
	}
	body, err := json.Marshal(providerRequest{Model: c.model, State: state, Questions: wire})
	if err != nil {
		return RawJudgments{}, err
	}
	response, err := c.callProvider(ctx, body)
	if err != nil {
		return RawJudgments{}, err
	}
	subsetSpec := QuestionSpec{SpecID: c.spec.SpecID, SpecVersion: c.spec.SpecVersion,
		TaxonomyVersion: c.spec.TaxonomyVersion, SemanticHash: c.spec.SemanticHash,
		Questions: questions, ScoreEnabled: c.spec.ScoreEnabled}
	if err := ValidateAnswers(subsetSpec, response.Answers); err != nil {
		return RawJudgments{}, enrich.Classified(err, enrich.ErrorClassContract)
	}
	judgments := RawJudgments{
		SpecID: c.spec.SpecID, SpecHash: c.spec.SemanticHash, TaxonomyVersion: c.spec.TaxonomyVersion,
		RequestedModel: c.model, ResolvedModel: response.Model,
		AliasDrift: response.Model != c.model, Judgments: map[string]RawJudgment{}, Coverage: "complete",
		EvidenceCoverage: evidence.Coverage, Truncated: evidence.Truncated,
		EvidenceHash: evidenceHash, QuestionHashes: map[string]string{},
		BatchSemantics: batchSemantics,
		Usage:          response.Usage, UsageMissing: response.UsageMissing,
	}
	// The resolved model is read from the response in postProvider; a mismatch is
	// recorded as drift.
	for _, question := range questions {
		answer := response.Answers[question.ID]
		judgment := RawJudgment{
			QuestionID: question.ID, Kind: question.Kind, Dimension: question.Dimension,
			TermID: question.TermID, Confidence: answer.Confidence,
		}
		switch question.Kind {
		case QuestionNoul:
			judgment.Noul = answer.Noul.Noul
		case QuestionChoice:
			judgment.Choice = answer.Choice.Choice
			judgment.Probabilities = answer.Choice.Probabilities
		case QuestionScore:
			judgment.Score = &answer.Score.Score
			judgment.Levels = legendOrdered(answer.Score.Legend)
			judgment.Probabilities = answer.Score.Probabilities
		}
		judgments.Judgments[question.ID] = judgment
		hash, _ := QuestionHash(question)
		judgments.QuestionHashes[question.ID] = hash
	}
	return judgments, nil
}
