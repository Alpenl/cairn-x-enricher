package classify

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// OverrideAction mirrors the Worker's field-level human actions.
type OverrideAction string

// Override actions mirror the Worker's field-level human actions.
const (
	OverrideAccept   OverrideAction = "accept"
	OverrideReject   OverrideAction = "reject"
	OverrideSetEmpty OverrideAction = "set_empty"
	OverrideReset    OverrideAction = "reset"
)

// Override is one human decision over a field. It is applied after the
// automatic proposal, so a rejection survives a policy replay that would
// otherwise re-propose the same value.
type Override struct {
	Field    string         `json:"field"`
	Term     string         `json:"term"`
	Action   OverrideAction `json:"action"`
	Source   string         `json:"source"`
	Revision int64          `json:"revision"`
}

// EffectiveView is the resolved, user-facing view. It is a pure function of the
// proposals and the overrides.
type EffectiveView struct {
	Topics   []string `json:"topics"`
	Form     string   `json:"form"`
	Use      string   `json:"use"`
	Entities []string `json:"entities"`
	// Empty records an explicit human "nothing applies" decision, which is
	// different from having no decision at all.
	Empty struct {
		Topics bool `json:"topics"`
		Form   bool `json:"form"`
		Use    bool `json:"use"`
	} `json:"empty"`
	Reviewed bool `json:"reviewed"`
}

// Resolve is a pure function. It applies overrides in revision order and never
// revives a rejected value, so replaying a policy cannot undo a human choice.
func Resolve(proposals Proposals, overrides []Override) EffectiveView {
	view := EffectiveView{
		Topics:   append([]string(nil), proposals.Topics...),
		Form:     proposals.Form,
		Use:      proposals.Use,
		Entities: []string{},
	}
	ordered := append([]Override(nil), overrides...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Revision < ordered[j].Revision })
	// Form and use are single-valued, so they are tracked separately from the
	// list fields: `reset` must restore the automatic proposal rather than clear
	// it.
	form, use := proposals.Form, proposals.Use
	formEmpty, useEmpty := false, false
	for _, override := range ordered {
		view.Reviewed = true
		switch override.Field {
		case "topic":
			view.Topics = applyScalar(view.Topics, override, &view.Empty.Topics)
		case "form":
			form, formEmpty = applySingle(form, proposals.Form, override)
		case "use":
			use, useEmpty = applySingle(use, proposals.Use, override)
		case "entity":
			view.Entities = applyScalar(view.Entities, override, nil)
		}
	}
	view.Form, view.Use = form, use
	view.Empty.Form, view.Empty.Use = formEmpty, useEmpty
	if view.Topics == nil {
		view.Topics = []string{}
	}
	if view.Entities == nil {
		view.Entities = []string{}
	}
	return view
}

func applyScalar(values []string, override Override, empty *bool) []string {
	switch override.Action {
	case OverrideAccept:
		if empty != nil {
			*empty = false
		}
		if !containsString(values, override.Term) {
			return append(values, override.Term)
		}
		return values
	case OverrideReject:
		return removeString(values, override.Term)
	case OverrideSetEmpty:
		if empty != nil {
			*empty = true
		}
		return []string{}
	case OverrideReset:
		if override.Term == "" {
			if empty != nil {
				*empty = false
			}
			// Resetting to automatic is expressed by the caller re-resolving
			// from the proposals; here it clears the explicit override effect.
			return nil
		}
		return removeString(values, override.Term)
	default:
		return values
	}
}

// applySingle applies one override to a single-valued field. `reset` restores
// the automatic proposal, which is why the automatic value is passed in.
func applySingle(current, automatic string, override Override) (string, bool) {
	switch override.Action {
	case OverrideAccept:
		return override.Term, false
	case OverrideReject:
		if current == override.Term {
			return "", false
		}
		return current, false
	case OverrideSetEmpty:
		return "", true
	case OverrideReset:
		if override.Term == "" || override.Term == current {
			return automatic, false
		}
		return current, false
	default:
		return current, false
	}
}

func removeString(values []string, want string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != want {
			out = append(out, value)
		}
	}
	return out
}

// ErrNoReplayableRun is returned when a replay is requested but the stored
// material cannot reconstruct the evaluated input. It is explicit rather than
// returning an empty result.
var ErrNoReplayableRun = errors.New("no stored run is available to replay")

// Replay re-decides stored raw judgments under a new policy without any network
// call. It is the mechanism behind zero-inference threshold changes.
func Replay(raw RawJudgments, oldPolicy, newPolicy Policy) (before, after Proposals, changed []string, err error) {
	if raw.SpecID == "" || len(raw.Judgments) == 0 {
		return Proposals{}, Proposals{}, nil, ErrNoReplayableRun
	}
	before, err = Decide(raw, oldPolicy)
	if err != nil {
		return Proposals{}, Proposals{}, nil, err
	}
	after, err = Decide(raw, newPolicy)
	if err != nil {
		return Proposals{}, Proposals{}, nil, err
	}
	changed = diffProposals(before, after)
	return before, after, changed, nil
}

func diffProposals(before, after Proposals) []string {
	changes := []string{}
	if !equalStrings(before.Topics, after.Topics) {
		changes = append(changes, "topics")
	}
	if before.Form != after.Form {
		changes = append(changes, "form")
	}
	if before.Use != after.Use {
		changes = append(changes, "use")
	}
	if len(before.Scores) != len(after.Scores) {
		changes = append(changes, "scores")
	}
	sort.Strings(changes)
	return changes
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// CacheKey identifies the exact inputs that produced a set of raw judgments.
// Two judgments may only be reused when every component matches: the evidence
// revision, the question definition, the candidates and the model. Reusing
// across a changed definition or a different model would mix results that are
// not comparable.
func CacheKey(evidenceHash, specHash, model, batchSemantics string) string {
	return evidenceHash + "|" + specHash + "|" + model + "|" + batchSemantics
}

// DecodeStoredJudgments rebuilds the replayable judgments from a stored run's
// answers. The stored answers are the typed transport form, so the policy can
// re-decide them without any model call. The spec is needed to know each
// question's kind and dimension; a run whose spec is unknown is not replayable.
func DecodeStoredJudgments(specID, specHash, requestedModel, resolvedModel string, answersJSON []byte, coverage string) (RawJudgments, error) {
	if len(answersJSON) == 0 {
		return RawJudgments{}, ErrNoReplayableRun
	}
	answers := map[string]RawAnswer{}
	if err := strictDecode(answersJSON, &answers); err != nil {
		return RawJudgments{}, fmt.Errorf("decode stored answers: %w", err)
	}
	if len(answers) == 0 {
		return RawJudgments{}, ErrNoReplayableRun
	}
	if coverage == "" {
		coverage = "complete"
	}
	judgments := RawJudgments{
		SpecID: specID, SpecHash: specHash, RequestedModel: requestedModel,
		ResolvedModel: resolvedModel, Coverage: coverage, Judgments: map[string]RawJudgment{},
	}
	for id, answer := range answers {
		judgment := RawJudgment{QuestionID: id, Confidence: answer.Confidence, Dimension: dimensionOf(id), TermID: termOf(id)}
		switch answer.Type {
		case TypeNoul:
			judgment.Kind = QuestionNoul
			judgment.Noul = answer.Noul.Noul
		case TypeChoice:
			judgment.Kind = QuestionChoice
			judgment.Choice = answer.Choice.Choice
			judgment.Probabilities = answer.Choice.Probabilities
		case TypeScore:
			judgment.Kind = QuestionScore
			score := answer.Score.Score
			judgment.Score = &score
			judgment.Levels = sortedCopy(answer.Score.Levels)
			judgment.Probabilities = answer.Score.Probabilities
		default:
			return RawJudgments{}, fmt.Errorf("stored answer %s has unknown type %q", id, answer.Type)
		}
		judgments.Judgments[id] = judgment
	}
	return judgments, nil
}

// dimensionOf maps a question ID back to its dimension. The ID is only a stable
// handle; this mapping exists so the policy can aggregate without the spec.
func dimensionOf(questionID string) string {
	switch {
	case strings.HasPrefix(questionID, "topic_"):
		return "topic"
	case questionID == "form":
		return "form"
	case questionID == "use":
		return "use"
	default:
		return questionID
	}
}

func termOf(questionID string) string {
	if strings.HasPrefix(questionID, "topic_") {
		return strings.TrimPrefix(questionID, "topic_")
	}
	return ""
}
