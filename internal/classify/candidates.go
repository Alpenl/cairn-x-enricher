package classify

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// CandidatePolicy controls objective lexical recall, never acceptance. The
// default client evaluates the whole catalog; this mode must be enabled
// explicitly after negotiating version-2 metadata with the store.
type CandidatePolicy struct {
	Version      string `json:"version"`
	MaxQuestions int    `json:"max_questions"`
}

// CandidatePolicyVersion identifies the deterministic recall and ranking rules.
const CandidatePolicyVersion = "topic-recall-v1"

// CandidateOmission records an unjudged question without manufacturing a Noul.
type CandidateOmission struct {
	QuestionID  string `json:"question_id"`
	Dimension   string `json:"dimension"`
	TermID      string `json:"term_id"`
	Granularity string `json:"granularity,omitempty"`
	Reason      string `json:"reason"`
}

// CandidateManifest binds the selected partition to the actual provider state
// and immutable full spec. Complete coverage means every selected question was
// answered; omitted questions remain unknown and have an explicit reason.
type CandidateManifest struct {
	Version             int                 `json:"version"`
	PolicyVersion       string              `json:"policy_version"`
	SpecHash            string              `json:"spec_hash"`
	StateHash           string              `json:"state_hash"`
	MaxQuestions        int                 `json:"max_questions"`
	SelectedQuestionIDs []string            `json:"selected_question_ids"`
	OmittedQuestionIDs  []string            `json:"omitted_question_ids"`
	Omitted             []CandidateOmission `json:"omitted"`
	SelectionHash       string              `json:"selection_hash,omitempty"`
}

// WithCandidatePolicy returns an independent client with explicit candidate
// recall enabled. It rejects a limit that could omit broad or non-topic core
// judgments; callers cannot silently turn core coverage into recall negatives.
func (c *Client) WithCandidatePolicy(policy CandidatePolicy) (*Client, error) {
	if err := validateCandidatePolicy(c.spec, policy); err != nil {
		return nil, err
	}
	copyClient := *c
	copyClient.candidatePolicy = &policy
	return &copyClient, nil
}

func validateCandidatePolicy(spec QuestionSpec, policy CandidatePolicy) error {
	if policy.Version != CandidatePolicyVersion || policy.MaxQuestions < 1 || policy.MaxQuestions > 128 {
		return errors.New("candidate policy must use topic-recall-v1 and a 1..128 question limit")
	}
	mandatory := 0
	for _, q := range spec.Questions {
		if q.Granularity != "specific" || normalizeDimension(q.Dimension) != "topics" {
			mandatory++
		}
	}
	if mandatory > policy.MaxQuestions {
		return errors.New("candidate limit would omit broad or non-topic core questions")
	}
	return nil
}

// PlanCandidates recalls specific topics from the bounded objective state.
// Broad topics and other dimensions stay covered. Related questions supply
// one-hop context; a relationship never implies an accepted label.
func PlanCandidates(spec QuestionSpec, evidence Evidence, policy CandidatePolicy) (CandidateManifest, error) {
	if err := validateCandidatePolicy(spec, policy); err != nil {
		return CandidateManifest{}, err
	}
	state, err := evidence.stateForModel()
	if err != nil {
		return CandidateManifest{}, err
	}
	text := recallText(evidence.Primary)
	for _, block := range evidence.Context {
		text += "\n" + recallText(block.Text)
	}
	type recalled struct {
		question Question
		hits     int
	}
	selected := map[string]bool{}
	byID := map[string]Question{}
	hits := map[string]int{}
	for _, q := range spec.Questions {
		if byID[q.ID].ID != "" {
			return CandidateManifest{}, errors.New("candidate spec has duplicate question identities")
		}
		byID[q.ID] = q
		if q.Granularity != "specific" || normalizeDimension(q.Dimension) != "topics" {
			selected[q.ID] = true
			continue
		}
		for _, phrase := range q.RecallTerms {
			if recallContains(text, recallText(phrase)) {
				hits[q.ID]++
			}
		}
	}
	// Snapshot direct hits before context expansion so relationships cannot
	// recursively flood the candidate set or inherit semantic membership.
	direct := map[string]int{}
	for id, count := range hits {
		direct[id] = count
	}
	for id := range direct {
		for _, related := range byID[id].RelatedQuestionIDs {
			q, ok := byID[related]
			if !ok {
				return CandidateManifest{}, fmt.Errorf("candidate context %s is not in the immutable spec", related)
			}
			if !selected[q.ID] && hits[q.ID] == 0 {
				hits[q.ID] = 0 // retain a context-only candidate with a zero lexical rank
			}
		}
	}
	var candidates []recalled
	for id, count := range hits {
		if !selected[id] {
			candidates = append(candidates, recalled{question: byID[id], hits: count})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].hits != candidates[j].hits {
			return candidates[i].hits > candidates[j].hits
		}
		return candidates[i].question.ID < candidates[j].question.ID
	})
	for _, candidate := range candidates {
		if len(selected) == policy.MaxQuestions {
			break
		}
		selected[candidate.question.ID] = true
	}
	manifest := CandidateManifest{
		Version: 1, PolicyVersion: policy.Version, SpecHash: spec.SemanticHash,
		StateHash: sha256Hex(state), MaxQuestions: policy.MaxQuestions,
		SelectedQuestionIDs: []string{}, OmittedQuestionIDs: []string{}, Omitted: []CandidateOmission{},
	}
	questions := append([]Question(nil), spec.Questions...)
	sort.Slice(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	for _, q := range questions {
		if selected[q.ID] {
			manifest.SelectedQuestionIDs = append(manifest.SelectedQuestionIDs, q.ID)
			continue
		}
		reason := "not_recalled"
		if _, recalled := hits[q.ID]; recalled {
			reason = "candidate_limit"
		}
		manifest.OmittedQuestionIDs = append(manifest.OmittedQuestionIDs, q.ID)
		manifest.Omitted = append(manifest.Omitted, CandidateOmission{
			QuestionID: q.ID, Dimension: q.Dimension, TermID: q.TermID, Granularity: q.Granularity, Reason: reason,
		})
	}
	manifest.SelectionHash, err = CandidateSelectionHash(manifest)
	return manifest, err
}

// CandidateSelectionHash matches Worker canonicalJSON scalar spelling and
// excludes the hash itself. Array order is part of the immutable identity.
func CandidateSelectionHash(manifest CandidateManifest) (string, error) {
	manifest.SelectionHash = ""
	encoded, err := canonicalReplayJSON(manifest)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

func recallText(text string) string {
	text = strings.Map(func(r rune) rune {
		if r >= 0xff01 && r <= 0xff5e {
			return unicode.ToLower(r - 0xfee0)
		}
		return unicode.ToLower(r)
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func recallContains(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	latin := true
	for _, r := range phrase {
		if r > unicode.MaxASCII {
			latin = false
			break
		}
	}
	if !latin {
		return strings.Contains(text, phrase)
	}
	word := func(r rune) bool {
		return r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_')
	}
	runes, match := []rune(text), []rune(phrase)
	for i := 0; i+len(match) <= len(runes); i++ {
		if string(runes[i:i+len(match)]) == phrase &&
			(i == 0 || !word(runes[i-1])) && (i+len(match) == len(runes) || !word(runes[i+len(match)])) {
			return true
		}
	}
	return false
}

func validateCandidateManifest(spec QuestionSpec, raw RawJudgments) (QuestionSpec, error) {
	if raw.MetadataVersion != 2 || raw.CandidateManifest == nil {
		return QuestionSpec{}, errors.New("candidate evaluation is missing its versioned manifest")
	}
	var state struct {
		Primary   string          `json:"primary"`
		Context   []EvidenceBlock `json:"context"`
		Coverage  string          `json:"coverage"`
		Truncated bool            `json:"truncated"`
	}
	if err := strictDecode([]byte(raw.WireState), &state); err != nil {
		return QuestionSpec{}, err
	}
	manifest := *raw.CandidateManifest
	plan, err := PlanCandidates(spec, Evidence{Primary: state.Primary, Context: state.Context, Coverage: state.Coverage, Truncated: state.Truncated},
		CandidatePolicy{Version: manifest.PolicyVersion, MaxQuestions: manifest.MaxQuestions})
	if err != nil {
		return QuestionSpec{}, err
	}
	if !reflect.DeepEqual(manifest, plan) || manifest.StateHash != raw.EvidenceHash {
		return QuestionSpec{}, errors.New("candidate manifest does not match immutable spec and objective state")
	}
	selected := map[string]bool{}
	for _, id := range manifest.SelectedQuestionIDs {
		selected[id] = true
	}
	subset := spec
	subset.Questions = nil
	for _, question := range spec.Questions {
		if selected[question.ID] {
			subset.Questions = append(subset.Questions, question)
		}
	}
	return subset, nil
}

func (c *Client) evaluateCandidates(ctx context.Context, input Input, previous *RawJudgments, batchSemantics string) (RawJudgments, error) {
	evidence, err := c.evidenceFor(input)
	if err != nil {
		return RawJudgments{}, err
	}
	manifest, err := PlanCandidates(c.spec, evidence, *c.candidatePolicy)
	if err != nil {
		return RawJudgments{}, err
	}
	selected := map[string]bool{}
	for _, id := range manifest.SelectedQuestionIDs {
		selected[id] = true
	}
	copyClient := *c
	copyClient.candidatePolicy = nil
	copyClient.spec.Questions = nil
	for _, question := range c.spec.Questions {
		if selected[question.ID] {
			copyClient.spec.Questions = append(copyClient.spec.Questions, question)
		}
	}
	var result Result
	if previous != nil {
		result, err = copyClient.ClassifyReusing(ctx, input, previous, batchSemantics)
	} else {
		result, err = copyClient.Classify(ctx, input)
	}
	raw := result.RawJudgments
	// Preserve receipts on failure; do not label an empty paid failure as a
	// complete candidate evaluation.
	if raw.SpecID != "" {
		raw.MetadataVersion = 2
		raw.CandidateManifest = &manifest
	}
	if err != nil {
		return raw, err
	}
	if _, validationErr := validateCandidateManifest(c.spec, raw); validationErr != nil {
		return raw, enrich.Classified(validationErr, enrich.ErrorClassContract)
	}
	return raw, nil
}
