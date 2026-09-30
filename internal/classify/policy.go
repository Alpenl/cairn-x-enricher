package classify

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// RawJudgment is one provider answer retained with its full distribution and
// the identity of the run that produced it. Nothing here is reduced to a
// verdict: the policy decides later, so a threshold change can replay this
// record without calling the model again.
type RawJudgment struct {
	QuestionID    string             `json:"question_id"`
	Kind          QuestionKind       `json:"kind"`
	Dimension     string             `json:"dimension"`
	TermID        string             `json:"term_id,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Levels is the ordered legend (index order) for a Score judgment.
	Levels     []string `json:"levels,omitempty"`
	Score      *float64 `json:"score,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// RawJudgments is the complete, replayable output of one evaluation. Coverage
// describes whether every question was answered; EvidenceCoverage records
// whether the objective input was truncated by the evidence budget. They are
// separate so a truncated input is never confused with an incomplete answer
// set, and both are stored with the run (F14).
type RawJudgments struct {
	// Version 1 binds persisted answers to the actual bounded provider state,
	// calls made by this run and the stored runs supplying reused questions.
	MetadataVersion int              `json:"metadata_version,omitempty"`
	WireState       string           `json:"wire_state,omitempty"`
	Calls           []ProviderCall   `json:"calls,omitempty"`
	ReusedFrom      map[string]int64 `json:"reused_from,omitempty"`
	// Assigned by the store reader, never trusted from serialized metadata.
	SourceRunID     int64  `json:"-"`
	SpecID          string `json:"spec_id"`
	SpecHash        string `json:"spec_hash"`
	TaxonomyVersion string `json:"taxonomy_version"`
	RequestedModel  string `json:"requested_model"`
	ResolvedModel   string `json:"resolved_model"`
	// AliasDrift is true when the provider resolved a different model than the
	// one requested (an alias moved). A calibrated policy must not silently
	// apply old thresholds to a drifted model.
	AliasDrift       bool                   `json:"alias_drift"`
	Judgments        map[string]RawJudgment `json:"judgments"`
	Coverage         string                 `json:"coverage"`
	EvidenceCoverage string                 `json:"evidence_coverage,omitempty"`
	Truncated        bool                   `json:"truncated,omitempty"`
	// EvidenceHash is the canonical hash of the objective evidence this run was
	// evaluated against. Per-question reuse is only legal when it matches.
	EvidenceHash string `json:"evidence_hash,omitempty"`
	// QuestionHashes records each question's semantic identity at evaluation
	// time, so a partial re-evaluation can reuse an unchanged question without
	// re-reading the historical spec.
	QuestionHashes map[string]string `json:"question_hashes,omitempty"`
	// Reused lists the questions whose answers were carried over from a
	// previous run instead of being inferred again.
	Reused []string `json:"reused,omitempty"`
	// Missing lists the questions a bounded batch could not answer. It is only
	// populated for partial coverage and never hidden behind a "complete" run.
	Missing []string `json:"missing,omitempty"`
	// BatchSemantics identifies how the questions were batched. A cached answer
	// may only be reused when it matches.
	BatchSemantics string `json:"batch_semantics,omitempty"`
	// Usage is the provider's raw usage object. UsageMissing distinguishes an
	// absent usage object from a genuine zero.
	Usage        json.RawMessage `json:"usage,omitempty"`
	UsageMissing bool            `json:"usage_missing,omitempty"`
}

// Policy is a versioned, pure decision rule. Changing thresholds or display
// rules produces a new policy version and a new decision over the same runs;
// it never produces a new model run.
type Policy struct {
	Version string `json:"version"`
	// Calibrated is false until versioned reference evaluation establishes the
	// thresholds. The evaluation artifact records human/automatic provenance;
	// fitting training data alone is not validation or permission to promote.
	Calibrated bool `json:"calibrated"`
	// TopicAccept is the probability at or above which a topic is accepted.
	TopicAccept float64 `json:"topic_accept"`
	// TopicReject is the probability below which a topic is rejected outright.
	// Between the two bounds the field abstains rather than being forced.
	TopicReject float64 `json:"topic_reject"`
	// ChoiceAccept and ChoiceMargin are legacy v1 selection rules retained for
	// the v1 projection only. They are not exposed as independent evidence.
	ChoiceAccept float64 `json:"choice_accept"`
	ChoiceMargin float64 `json:"choice_margin"`
	// MaxDisplayTopics bounds the card display. It never deletes a fourth
	// effective topic; it only folds the card.
	MaxDisplayTopics int `json:"max_display_topics"`
	// MaxEffectiveTopics bounds how many topics may be effective. It is a
	// separate safety limit from display.
	MaxEffectiveTopics int `json:"max_effective_topics"`
	// AllowAliasDrift opts a calibrated policy into running against a resolved
	// model that differs from the requested one. It is false by default so a
	// provider alias change cannot inherit old calibration silently.
	AllowAliasDrift bool `json:"allow_alias_drift"`
	// BlockPersonalUse prevents objective judgments from proposing reserved
	// personal uses. Absence preserves historical policy replay; current policy
	// versions enable it and the Worker independently rejects such new writes.
	BlockPersonalUse bool `json:"block_personal_use,omitempty"`
	// Primary tags are the visible objective topics, resource kinds and content
	// functions. The minimum is a supported target, never permission to invent
	// labels; the maximum applies before human overrides.
	MinPrimaryTags int `json:"min_primary_tags,omitempty"`
	MaxPrimaryTags int `json:"max_primary_tags,omitempty"`
	// FunctionSupportAccept is the weaker, explicit support bound used only to
	// fill a sparse automatic result with existing content-function judgments.
	FunctionSupportAccept float64 `json:"function_support_accept,omitempty"`
}

// DefaultPolicy is the conservative, explicitly uncalibrated objective policy.
func DefaultPolicy() Policy {
	return Policy{
		Version: PolicyVersion, Calibrated: false, BlockPersonalUse: true,
		TopicAccept: 0.8, TopicReject: 0.2,
		ChoiceAccept: 0.65, ChoiceMargin: 0.15,
		MaxDisplayTopics: 3, MaxEffectiveTopics: 64,
		MinPrimaryTags: 2, MaxPrimaryTags: 5, FunctionSupportAccept: 0.65,
	}
}

// Validate rejects a policy whose bounds cannot describe a decision.
func (p Policy) Validate() error {
	if (p.Version == "jev-policy-v3" || p.Version == "jev-policy-v4") && !p.BlockPersonalUse {
		return errors.New("current objective policies require the personal use guard")
	}
	if p.Version == "jev-policy-v2" && p.BlockPersonalUse {
		return errors.New("personal use guard changes jev-policy-v2 semantics; use a new policy identity")
	}
	hasDensity := p.MinPrimaryTags != 0 || p.MaxPrimaryTags != 0 || p.FunctionSupportAccept != 0
	if (p.Version == "jev-policy-v2" || p.Version == "jev-policy-v3") && hasDensity {
		return errors.New("primary tag density changes historical policy semantics; use a new policy identity")
	}
	if p.Version == "jev-policy-v4" || hasDensity {
		if p.MinPrimaryTags < 1 || p.MaxPrimaryTags < 2 || p.MinPrimaryTags > p.MaxPrimaryTags || p.MaxPrimaryTags > 64 {
			return errors.New("primary tag density limits are inconsistent")
		}
		if !validProbability(p.FunctionSupportAccept) || p.FunctionSupportAccept <= p.TopicReject || p.FunctionSupportAccept > p.TopicAccept {
			return errors.New("content function support must be above rejection and at or below acceptance")
		}
	}
	if p.Version == "" || len(p.Version) > 100 {
		return errors.New("policy version must contain 1 to 100 bytes")
	}
	if !validProbability(p.TopicAccept) || !validProbability(p.TopicReject) {
		return errors.New("topic bounds must be probabilities")
	}
	if p.TopicReject > p.TopicAccept {
		return errors.New("topic reject bound must not exceed the accept bound")
	}
	if !validProbability(p.ChoiceAccept) || !validProbability(p.ChoiceMargin) {
		return errors.New("choice rules must be probabilities")
	}
	if p.MaxDisplayTopics < 1 || p.MaxEffectiveTopics < 1 || p.MaxDisplayTopics > p.MaxEffectiveTopics {
		return errors.New("display and effective topic limits are inconsistent")
	}
	return nil
}

// Verdict is the per-field outcome. Abstained is distinct from Rejected: an
// abstained field has no basis for a decision, while a rejected field has
// positive evidence against it. Neither is a failure.
type Verdict string

// Verdicts. Abstained and Rejected are deliberately distinct.
const (
	VerdictAccepted  Verdict = "accepted"
	VerdictRejected  Verdict = "rejected"
	VerdictAbstained Verdict = "abstained"
)

// FieldDecision is the decision for one dimension (or one candidate).
type FieldDecision struct {
	Dimension string  `json:"dimension"`
	TermID    string  `json:"term_id,omitempty"`
	Verdict   Verdict `json:"verdict"`
	// Value is the accepted value for a single-value dimension.
	Value string `json:"value,omitempty"`
	// Candidate retains a Choice winner even when the policy abstains.
	Candidate string `json:"candidate,omitempty"`
	// Reason is derived from observable evidence or an explicit judgment, never
	// guessed from an intermediate probability.
	Reason string `json:"reason"`
	// Probability is retained for audit only.
	Probability float64 `json:"probability"`
}

// Proposals is the pure output of Decide. The multidimensional fields are the
// automatic baseline the Worker stores with the decision; human overrides are
// applied afterwards by Resolve.
type Proposals struct {
	PolicyVersion    string             `json:"policy_version"`
	SpecID           string             `json:"spec_id"`
	Decisions        []FieldDecision    `json:"decisions"`
	Topics           []string           `json:"topics"`
	ResourceKinds    []string           `json:"resource_kinds"`
	ContentFunctions []string           `json:"content_functions"`
	Carriers         []string           `json:"carriers"`
	Affordances      []string           `json:"affordances"`
	Entities         []string           `json:"entities"`
	Form             string             `json:"form"`
	Use              string             `json:"use"`
	Scores           map[string]float64 `json:"scores,omitempty"`
	// Incomplete records which dimensions produced no decision, so an empty
	// result can be distinguished from a not-run or failed one.
	Incomplete []string `json:"incomplete"`
}

// Decide is a pure function: it reads only the raw judgments and the policy.
// It never reads the network, the clock or global state, so replaying it over
// stored judgments is deterministic.
func Decide(raw RawJudgments, policy Policy) (Proposals, error) {
	if err := policy.Validate(); err != nil {
		return Proposals{}, err
	}
	// A partial run may still be decided over the fields it does have, but the
	// missing questions are recorded so the result can never masquerade as a
	// complete evaluation (R2-13).
	if raw.Coverage != "complete" && raw.Coverage != "partial" {
		return Proposals{}, fmt.Errorf("decide requires complete or partial coverage, got %q", raw.Coverage)
	}
	// A drifted alias must not inherit a calibrated policy: the calibration was
	// measured on a specific resolved model. Replaying an uncalibrated policy is
	// still allowed because it makes no accuracy claim.
	if raw.AliasDrift && policy.Calibrated && !policy.AllowAliasDrift {
		return Proposals{}, fmt.Errorf("resolved model %q differs from requested %q; a calibrated policy requires an explicit drift opt-in",
			raw.ResolvedModel, raw.RequestedModel)
	}
	proposals := Proposals{PolicyVersion: policy.Version, SpecID: raw.SpecID, Decisions: []FieldDecision{}, Incomplete: []string{}}
	// Multi-valued Noul dimensions are collected per dimension so the topic
	// safety limit cannot accidentally bound a different dimension.
	type candidate struct {
		term string
		p    float64
	}
	candidatesByDimension := map[string][]candidate{}
	dimensionSeen := map[string]bool{}
	for _, judgment := range raw.Judgments {
		dimension := normalizeDimension(judgment.Dimension)
		dimensionSeen[dimension] = true
		switch judgment.Kind {
		case QuestionNoul:
			if judgment.Noul == nil {
				return Proposals{}, fmt.Errorf("judgment %s has no probability", judgment.QuestionID)
			}
			p := *judgment.Noul
			switch {
			case p >= policy.TopicAccept:
				candidatesByDimension[dimension] = append(candidatesByDimension[dimension],
					candidate{term: judgment.TermID, p: p})
			case p <= policy.TopicReject:
				reason := "evidence is present but the topic is not substantively discussed"
				if dimension == "resource_kinds" {
					reason = "evidence is present but does not establish this reusable resource"
				}
				proposals.Decisions = append(proposals.Decisions, FieldDecision{
					Dimension: dimension, TermID: judgment.TermID, Verdict: VerdictRejected,
					Reason: reason, Probability: p,
				})
			default:
				// A single ambiguous candidate abstains locally and does not
				// make the whole record uncertain.
				proposals.Decisions = append(proposals.Decisions, FieldDecision{
					Dimension: dimension, TermID: judgment.TermID, Verdict: VerdictAbstained,
					Reason: "probability falls between the reject and accept bounds", Probability: p,
				})
			}
		case QuestionChoice:
			if judgment.Choice == "" || judgment.Probabilities == nil {
				return Proposals{}, fmt.Errorf("judgment %s has no choice", judgment.QuestionID)
			}
			feature := DescribeDistribution(judgment.Probabilities)
			value := ""
			reason := ""
			verdict := VerdictAbstained
			best := judgment.Probabilities[judgment.Choice]
			// A legitimate none/empty is a completed answer, not an abstention.
			if policy.BlockPersonalUse && dimension == "use" && taxonomy.PersonalUse(judgment.Choice) {
				reason = "personal opposition requires explicit human input; objective evidence cannot establish it"
			} else if judgment.Choice == "none" {
				verdict = VerdictAccepted
				reason = "the model explicitly judged that no option applies"
			} else if best >= policy.ChoiceAccept {
				value = judgment.Choice
				verdict = VerdictAccepted
				reason = "the chosen option is the distribution maximum above the accept threshold"
			} else {
				reason = "no option reached the accept threshold"
			}
			proposals.Decisions = append(proposals.Decisions, FieldDecision{
				Dimension: dimension, TermID: judgment.TermID, Verdict: verdict, Value: value,
				Candidate: judgment.Choice,
				Reason:    reason, Probability: feature.MaxProb,
			})
		case QuestionScore:
			if judgment.Score == nil {
				return Proposals{}, fmt.Errorf("judgment %s has no score", judgment.QuestionID)
			}
			if proposals.Scores == nil {
				proposals.Scores = map[string]float64{}
			}
			// The score is stored with its distribution; the distribution is
			// not collapsed into confidence. It is a probability-weighted float
			// that may fall between levels.
			proposals.Scores[judgment.QuestionID] = *judgment.Score
			proposals.Decisions = append(proposals.Decisions, FieldDecision{
				Dimension: dimension, Verdict: VerdictAccepted,
				Reason: "ordinal score with a retained distribution", Probability: DescribeDistribution(judgment.Probabilities).MaxProb,
			})
		}
	}
	// Rank each dimension by probability with a deterministic tie-break so the
	// same judgments always produce the same order.
	rank := func(values []candidate) []candidate {
		ordered := append([]candidate(nil), values...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].p == ordered[j].p {
				return ordered[i].term < ordered[j].term
			}
			return ordered[i].p > ordered[j].p
		})
		return ordered
	}
	topics := rank(candidatesByDimension["topics"])
	if len(topics) > policy.MaxEffectiveTopics {
		for _, dropped := range topics[policy.MaxEffectiveTopics:] {
			proposals.Decisions = append(proposals.Decisions, FieldDecision{
				Dimension: "topics", TermID: dropped.term, Verdict: VerdictAbstained,
				Reason: "beyond the effective topic safety limit", Probability: dropped.p,
			})
		}
		topics = topics[:policy.MaxEffectiveTopics]
	}
	for _, item := range topics {
		proposals.Topics = append(proposals.Topics, item.term)
		proposals.Decisions = append(proposals.Decisions, FieldDecision{
			Dimension: "topics", TermID: item.term, Verdict: VerdictAccepted,
			Reason: "topic probability is at or above the accept bound", Probability: item.p,
		})
	}
	multiDimensions := []struct {
		name   string
		target *[]string
	}{
		{"resource_kinds", &proposals.ResourceKinds},
		{"content_functions", &proposals.ContentFunctions},
		{"affordances", &proposals.Affordances},
	}
	for _, dimension := range multiDimensions {
		for _, item := range rank(candidatesByDimension[dimension.name]) {
			*dimension.target = append(*dimension.target, item.term)
			proposals.Decisions = append(proposals.Decisions, FieldDecision{
				Dimension: dimension.name, TermID: item.term, Verdict: VerdictAccepted,
				Reason: "probability is at or above the accept bound", Probability: item.p,
			})
		}
	}
	if policy.MaxPrimaryTags > 0 {
		applyPrimaryTagDensity(&proposals, raw, policy)
	}
	// Legacy dimensions that keep the v1 projection alive.
	for _, decision := range proposals.Decisions {
		switch {
		case decision.Dimension == "form" && decision.Verdict == VerdictAccepted:
			proposals.Form = decision.Value
		case decision.Dimension == "use" && decision.Verdict == VerdictAccepted:
			proposals.Use = decision.Value
		case decision.Dimension == "carriers" && decision.Verdict == VerdictAccepted && decision.Value != "":
			proposals.Carriers = []string{decision.Value}
		}
	}
	for _, dimension := range []string{"topics", "form", "use"} {
		if !dimensionSeen[dimension] {
			proposals.Incomplete = append(proposals.Incomplete, dimension)
		}
	}
	if raw.Coverage == "partial" {
		for _, questionID := range raw.Missing {
			proposals.Incomplete = append(proposals.Incomplete, "missing:"+questionID)
		}
	}
	sort.Strings(proposals.Incomplete)
	// These outcomes now participate in immutable request identities. Go map
	// iteration must not change an otherwise identical retry/replay payload.
	sort.Slice(proposals.Decisions, func(i, j int) bool {
		a, b := proposals.Decisions[i], proposals.Decisions[j]
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		if a.TermID != b.TermID {
			return a.TermID < b.TermID
		}
		if a.Candidate != b.Candidate {
			return a.Candidate < b.Candidate
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		if a.Verdict != b.Verdict {
			return a.Verdict < b.Verdict
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Probability < b.Probability
	})
	return proposals, nil
}

// applyPrimaryTagDensity composes retained judgments without inferring new
// semantics. Resolve runs afterwards and is allowed to produce fewer or more
// visible labels when a person explicitly clears, rejects or adds them.
func applyPrimaryTagDensity(proposals *Proposals, raw RawJudgments, policy Policy) {
	primary := func(dimension string) bool {
		return dimension == "topics" || dimension == "resource_kinds" || dimension == "content_functions"
	}
	less := func(a, b FieldDecision) bool {
		if a.Probability != b.Probability {
			return a.Probability > b.Probability
		}
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		return a.TermID < b.TermID
	}
	count := len(proposals.Topics) + len(proposals.ResourceKinds) + len(proposals.ContentFunctions)
	if count < policy.MinPrimaryTags {
		// Only observed Noul function judgments may support the minimum. Choice,
		// personal-use and missing answers cannot be repurposed into labels.
		observed := map[string]bool{}
		for _, judgment := range raw.Judgments {
			if judgment.Kind == QuestionNoul && normalizeDimension(judgment.Dimension) == "content_functions" &&
				judgment.Noul != nil && validProbability(*judgment.Noul) {
				observed[judgment.TermID] = true
			}
		}
		var support []int
		for i, decision := range proposals.Decisions {
			if decision.Dimension == "content_functions" && decision.TermID != "" && observed[decision.TermID] &&
				decision.Verdict == VerdictAbstained && decision.Probability >= policy.FunctionSupportAccept {
				support = append(support, i)
			}
		}
		sort.Slice(support, func(i, j int) bool { return less(proposals.Decisions[support[i]], proposals.Decisions[support[j]]) })
		for _, index := range support {
			if count >= policy.MinPrimaryTags {
				break
			}
			decision := &proposals.Decisions[index]
			decision.Verdict = VerdictAccepted
			decision.Reason = "density_support"
			proposals.ContentFunctions = append(proposals.ContentFunctions, decision.TermID)
			count++
		}
	}
	if count <= policy.MaxPrimaryTags {
		return
	}
	var accepted []int
	for i, decision := range proposals.Decisions {
		if primary(decision.Dimension) && decision.TermID != "" && decision.Verdict == VerdictAccepted {
			accepted = append(accepted, i)
		}
	}
	sort.Slice(accepted, func(i, j int) bool { return less(proposals.Decisions[accepted[i]], proposals.Decisions[accepted[j]]) })
	key := func(decision FieldDecision) string { return decision.Dimension + "/" + decision.TermID }
	kept := map[string]bool{}
	// Preserve a substantive subject and reusable resource whenever either was
	// accepted, even if higher-probability functions compete for the same slots.
	for _, dimension := range []string{"topics", "resource_kinds"} {
		for _, index := range accepted {
			decision := proposals.Decisions[index]
			if decision.Dimension == dimension {
				kept[key(decision)] = true
				break
			}
		}
	}
	for _, index := range accepted {
		decision := &proposals.Decisions[index]
		if !kept[key(*decision)] && len(kept) < policy.MaxPrimaryTags {
			kept[key(*decision)] = true
		}
	}
	for _, index := range accepted {
		decision := &proposals.Decisions[index]
		if !kept[key(*decision)] {
			decision.Verdict = VerdictAbstained
			decision.Reason = "label_limit"
		}
	}
	retain := func(dimension string, values []string) []string {
		result := values[:0]
		for _, term := range values {
			if kept[dimension+"/"+term] {
				result = append(result, term)
			}
		}
		return result
	}
	proposals.Topics = retain("topics", proposals.Topics)
	proposals.ResourceKinds = retain("resource_kinds", proposals.ResourceKinds)
	proposals.ContentFunctions = retain("content_functions", proposals.ContentFunctions)
}

// normalizeDimension maps the legacy singular dimension names onto the v2
// vocabulary so a stored run produced before the rename still decides through
// the same policy path.
func normalizeDimension(dimension string) string {
	switch dimension {
	case "topic":
		return "topics"
	case "content_function":
		return "content_functions"
	case "carrier":
		return "carriers"
	case "affordance":
		return "affordances"
	case "entity":
		return "entities"
	default:
		return dimension
	}
}

// PartitionTopics splits the effective topics into the ones shown on a card and
// the ones folded behind it. Display folding never deletes a topic.
func PartitionTopics(topics []string, policy Policy) (shown, folded []string) {
	if len(topics) <= policy.MaxDisplayTopics {
		return append([]string(nil), topics...), []string{}
	}
	return append([]string(nil), topics[:policy.MaxDisplayTopics]...), append([]string(nil), topics[policy.MaxDisplayTopics:]...)
}

// MarginHasNoIndependentPower documents and checks the v1 margin rule. On a
// normalized distribution, `best - second >= 0.15` is implied by
// `best >= 0.65`, so the margin adds no independent filter. Tests assert both
// directions of that implication.
func MarginHasNoIndependentPower(best, second, accept, margin float64) bool {
	return math.Abs((accept-margin)-0.5) < 1e-9 && best >= accept && second <= 1-best
}
