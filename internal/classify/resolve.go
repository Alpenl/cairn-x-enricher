package classify

import (
	"errors"
	"fmt"
	"slices"
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
//
// Multi-valued fields (topics, content_functions, affordances, entities)
// accept by accumulation. Single-valued fields (carriers, form, use) accept by
// replacement: accepting a second value does not silently keep the automatic
// one (F11).
type EffectiveView struct {
	Topics           []string `json:"topics"`
	ContentFunctions []string `json:"content_functions"`
	Carriers         []string `json:"carriers"`
	Affordances      []string `json:"affordances"`
	Form             string   `json:"form"`
	Use              string   `json:"use"`
	Entities         []string `json:"entities"`
	// Empty records an explicit human "nothing applies" decision, which is
	// different from having no decision at all.
	Empty struct {
		Topics           bool `json:"topics"`
		ContentFunctions bool `json:"content_functions"`
		Carriers         bool `json:"carriers"`
		Affordances      bool `json:"affordances"`
		Form             bool `json:"form"`
		Use              bool `json:"use"`
	} `json:"empty"`
	Reviewed bool `json:"reviewed"`
}

// effectiveFields are the field names shared with the Worker. The list is the
// single source of truth for the dimension set so Go and TypeScript cannot
// drift into "topic" vs "topics" style incompatibilities (F04).
var effectiveFields = []string{"topics", "content_functions", "carriers", "affordances", "form", "use"}

// OverrideVector is one shared, vocabulary-independent override action.
type OverrideVector struct {
	Field  string         `json:"field"`
	Term   string         `json:"term"`
	Action OverrideAction `json:"action"`
}

// fieldState is the deterministic per-tag override state. It is the shared
// model behind Resolve and the Worker's resolveEffective, verified against the
// same golden vectors.
type fieldState struct {
	// action is the latest explicit action per term. Terms absent from the map
	// inherit the automatic value.
	action map[string]OverrideAction
	// order records accept order so appended multi-values are deterministic.
	order []string
	// history preserves single-value replacements in actual action order. A map
	// of latest actions per term cannot represent A -> B -> A or reject-active.
	history []OverrideVector
	// empty records an explicit "nothing applies" decision. It excludes the
	// automatic values until a per-tag reset re-admits one or a full reset
	// returns to automatic.
	empty bool
	// clearedAutomatic suppresses automatic values after set_empty, so accepting
	// a new tag afterwards does not silently restore the cleared automatic tags.
	clearedAutomatic bool
	readmit          map[string]bool
}

func newFieldState() *fieldState {
	return &fieldState{action: map[string]OverrideAction{}, readmit: map[string]bool{}}
}

func (s *fieldState) apply(override Override) {
	switch override.Action {
	case OverrideAccept:
		s.history = append(s.history, OverrideVector{Term: override.Term, Action: override.Action})
		if _, exists := s.action[override.Term]; !exists {
			s.order = append(s.order, override.Term)
		}
		s.action[override.Term] = OverrideAccept
		s.empty = false
	case OverrideReject:
		s.history = append(s.history, OverrideVector{Term: override.Term, Action: override.Action})
		if _, exists := s.action[override.Term]; !exists {
			s.order = append(s.order, override.Term)
		}
		s.action[override.Term] = OverrideReject
		s.empty = false
	case OverrideSetEmpty:
		s.action = map[string]OverrideAction{}
		s.order = nil
		s.history = nil
		s.empty = true
		s.clearedAutomatic = true
		s.readmit = map[string]bool{}
	case OverrideReset:
		if override.Term == "" {
			s.action = map[string]OverrideAction{}
			s.order = nil
			s.history = nil
			s.empty = false
			s.clearedAutomatic = false
			s.readmit = map[string]bool{}
			return
		}
		// A per-tag reset removes exactly that tag's override so the tag falls
		// back to the automatic value. Clearing the whole field would be wrong.
		delete(s.action, override.Term)
		filtered := s.order[:0]
		for _, term := range s.order {
			if term != override.Term {
				filtered = append(filtered, term)
			}
		}
		s.order = filtered
		remaining := s.history[:0]
		for _, action := range s.history {
			if action.Term != override.Term {
				remaining = append(remaining, action)
			}
		}
		s.history = remaining
		if s.clearedAutomatic {
			// The tag this reset names becomes eligible again from automatic.
			s.readmit[override.Term] = true
		}
	}
}

// resolveMulti resolves a multi-valued field: accepted terms accumulate, rejected
// terms are removed and a reset restores the automatic value.
func (s *fieldState) resolveMulti(automatic []string) []string {
	if s.empty {
		return []string{}
	}
	out := make([]string, 0, len(automatic)+len(s.order))
	seen := map[string]bool{}
	add := func(term string) {
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	for _, term := range automatic {
		if s.action[term] == OverrideReject {
			continue
		}
		if s.clearedAutomatic && !s.readmit[term] {
			continue
		}
		add(term)
	}
	for _, term := range s.order {
		if s.action[term] == OverrideAccept {
			add(term)
		}
	}
	return out
}

// resolveSingle resolves a single-valued field. An accept replaces both the
// automatic value and any previous accept; a reject of the active value clears
// it; a reset restores the automatic value.
func (s *fieldState) resolveSingle(automatic string) string {
	if s.empty {
		return ""
	}
	current := automatic
	if s.clearedAutomatic && !s.readmit[automatic] {
		current = ""
	}
	for _, action := range s.history {
		term := action.Term
		switch action.Action {
		case OverrideAccept:
			current = term
		case OverrideReject:
			if current == term {
				current = ""
			}
		}
	}
	// A rejection recorded before any accept must still apply to the automatic
	// value (the order slice records both).
	if automatic != "" && s.action[automatic] == OverrideReject && current == automatic {
		current = ""
	}
	return current
}

func (s *fieldState) resolvedEmpty() bool { return s.empty }

// resolveCarrier resolves the single-valued carrier dimension. The effective
// view keeps it as a list because the stored selection and the API both model
// it as a 0- or 1-element array.
func resolveCarrier(automatic []string, state *fieldState) []string {
	first := ""
	if len(automatic) > 0 {
		first = automatic[0]
	}
	value := state.resolveSingle(first)
	if value == "" {
		return []string{}
	}
	return []string{value}
}

// Resolve is a pure function. It applies overrides in revision order and never
// revives a rejected value, so replaying a policy cannot undo a human choice.
func Resolve(proposals Proposals, overrides []Override) EffectiveView {
	ordered := append([]Override(nil), overrides...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Revision < ordered[j].Revision })
	states := map[string]*fieldState{}
	for _, field := range effectiveFields {
		states[field] = newFieldState()
	}
	// Entity overrides keep using their dedicated field name.
	states["entities"] = newFieldState()
	for _, override := range ordered {
		field := normalizeField(override.Field)
		state, ok := states[field]
		if !ok {
			continue
		}
		state.apply(override)
	}
	view := EffectiveView{
		Topics:           states["topics"].resolveMulti(proposals.Topics),
		ContentFunctions: states["content_functions"].resolveMulti(proposals.ContentFunctions),
		// Carrier is single-valued: an accept replaces the automatic carrier.
		Carriers:    resolveCarrier(proposals.Carriers, states["carriers"]),
		Affordances: states["affordances"].resolveMulti(proposals.Affordances),
		Form:        states["form"].resolveSingle(proposals.Form),
		Use:         states["use"].resolveSingle(proposals.Use),
		Entities:    states["entities"].resolveMulti(proposals.Entities),
	}
	for _, state := range states {
		if len(state.action) > 0 || state.clearedAutomatic || state.empty {
			view.Reviewed = true
			break
		}
	}
	view.Empty.Topics = states["topics"].resolvedEmpty()
	view.Empty.ContentFunctions = states["content_functions"].resolvedEmpty()
	view.Empty.Carriers = states["carriers"].resolvedEmpty()
	view.Empty.Affordances = states["affordances"].resolvedEmpty()
	view.Empty.Form = states["form"].resolvedEmpty()
	view.Empty.Use = states["use"].resolvedEmpty()
	return view
}

// normalizeField accepts the legacy singular alias `topic` plus `entity` so
// stored v1 rows keep resolving, while the v2 vocabulary is plural.
func normalizeField(field string) string {
	switch field {
	case "topic":
		return "topics"
	case "entity":
		return "entities"
	case "content_function":
		return "content_functions"
	case "carrier":
		return "carriers"
	case "affordance":
		return "affordances"
	default:
		return field
	}
}

// ErrNoReplayableRun is returned when a replay is requested but the stored
// material cannot reconstruct the evaluated input. It is explicit rather than
// returning an empty result.
var ErrNoReplayableRun = errors.New("no stored run is available to replay")

// ErrHistoricalPolicyMissing is returned when the stored run cannot be paired
// with the policy that produced it. Replaying it under an unrelated default and
// renaming the version would fabricate a baseline, so it fails instead (F06).
var ErrHistoricalPolicyMissing = errors.New("stored run has no recoverable historical policy")

// Replay re-decides stored raw judgments under a new policy without any network
// call. It is the mechanism behind zero-inference threshold changes.
func Replay(raw RawJudgments, oldPolicy, newPolicy Policy) (before, after Proposals, changed []string, err error) {
	if raw.SpecID == "" || len(raw.Judgments) == 0 {
		return Proposals{}, Proposals{}, nil, ErrNoReplayableRun
	}
	if oldPolicy.Version == "" {
		return Proposals{}, Proposals{}, nil, ErrHistoricalPolicyMissing
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
	return slices.Equal(left, right)
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
// answers and the immutable spec they were produced from. The spec is required:
// deriving the dimension or the level order from the question ID would guess at
// meanings the run never recorded (F06).
func DecodeStoredJudgments(spec QuestionSpec, requestedModel, resolvedModel string, answersJSON []byte, coverage string) (RawJudgments, error) {
	specByID := map[string]Question{}
	for _, question := range spec.Questions {
		specByID[question.ID] = question
	}
	answers, err := DecodeAnswers(answersJSON)
	if err != nil {
		return RawJudgments{}, fmt.Errorf("decode stored answers: %w", err)
	}
	if len(answers) == 0 {
		return RawJudgments{}, ErrNoReplayableRun
	}
	if err := ValidateAnswers(spec, answers); err != nil {
		return RawJudgments{}, fmt.Errorf("stored answers do not match their spec: %w", err)
	}
	if coverage == "" {
		coverage = "complete"
	}
	judgments := RawJudgments{
		SpecID: spec.SpecID, SpecHash: spec.SemanticHash, TaxonomyVersion: spec.TaxonomyVersion,
		RequestedModel: requestedModel, ResolvedModel: resolvedModel,
		AliasDrift: resolvedModel != "" && requestedModel != "" && resolvedModel != requestedModel,
		Coverage:   coverage, Judgments: map[string]RawJudgment{},
	}
	for id, answer := range answers {
		question, ok := specByID[id]
		if !ok {
			return RawJudgments{}, fmt.Errorf("stored answer %s has no question in spec %s", id, spec.SpecID)
		}
		judgment := RawJudgment{
			QuestionID: id, Kind: question.Kind, Dimension: question.Dimension,
			TermID: question.TermID, Confidence: answer.Confidence,
		}
		switch answer.Type {
		case TypeNoul:
			judgment.Noul = answer.Noul.Noul
		case TypeChoice:
			judgment.Choice = answer.Choice.Choice
			judgment.Probabilities = answer.Choice.Probabilities
		case TypeScore:
			judgment.Score = &answer.Score.Score
			judgment.Levels = legendOrdered(answer.Score.Legend)
			judgment.Probabilities = answer.Score.Probabilities
		default:
			return RawJudgments{}, fmt.Errorf("stored answer %s has unknown type %q", id, answer.Type)
		}
		judgments.Judgments[id] = judgment
	}
	return judgments, nil
}

// DecodePolicy parses a stored policy payload. An empty payload means the
// historical policy is unknown and must not be replaced by a default.
func DecodePolicy(payload []byte) (Policy, error) {
	if len(payload) == 0 || strings.TrimSpace(string(payload)) == "null" {
		return Policy{}, ErrHistoricalPolicyMissing
	}
	var policy Policy
	if err := strictDecode(payload, &policy); err != nil {
		return Policy{}, fmt.Errorf("decode stored policy: %w", err)
	}
	if policy.Version == "" {
		return Policy{}, ErrHistoricalPolicyMissing
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}
