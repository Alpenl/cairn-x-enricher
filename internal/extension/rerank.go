package extension

import (
	"errors"
	"sort"
	"strings"
)

// Candidate is one rerankable result. The candidate set is produced and
// authorized by the caller; reranking may never add, remove or reveal one.
type Candidate struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Rank      int        `json:"rank"`
	Role      string     `json:"role,omitempty"`
	Allowed   bool       `json:"allowed"`
	CacheItem *CacheItem `json:"-"`
}

// RerankScore is the model's comparable judgment for one candidate on a shared
// rubric. It is not the Noul probability of a different proposition: the rubric
// and query are held constant across candidates.
type RerankScore struct {
	ID    string `json:"id"`
	Score int    `json:"score"`
}

// RerankResult reports the outcome and why it happened, so a fallback is
// visible instead of looking like a real ranking.
type RerankResult struct {
	Candidates   []Candidate `json:"candidates"`
	Applied      bool        `json:"applied"`
	Reason       string      `json:"reason"`
	CacheStatus  string      `json:"cache_status,omitempty"`
	Scope        string      `json:"scope,omitempty"`
	NextBeforeID *int64      `json:"next_before_id"`
}

// Rerank orders candidates by the model score with a stable tie-break on the
// original rank. Permission and filtering are never changed: a candidate the
// caller removed is not reintroduced, and a denied candidate is not returned.
func Rerank(candidates []Candidate, scores []RerankScore, enabled bool) RerankResult {
	// Only candidates the caller already allowed participate.
	allowed := make([]Candidate, 0, len(candidates))
	denied := make([]Candidate, 0)
	for _, candidate := range candidates {
		if candidate.Allowed {
			allowed = append(allowed, candidate)
		} else {
			denied = append(denied, candidate)
		}
	}
	if !enabled {
		// A disabled extension falls back to the original order, including the
		// entries the caller filtered out (they are simply not returned).
		return RerankResult{Candidates: allowed, Applied: false, Reason: "rerank disabled"}
	}
	if len(scores) == 0 {
		return RerankResult{Candidates: allowed, Applied: false, Reason: "no scores available"}
	}
	byID := map[string]int{}
	for _, score := range scores {
		byID[score.ID] = score.Score
	}
	// A score for a candidate outside the authorized set is ignored, never
	// used to inject a new result.
	scored := 0
	for _, candidate := range allowed {
		if _, ok := byID[candidate.ID]; ok {
			scored++
		}
	}
	if scored == 0 {
		return RerankResult{Candidates: allowed, Applied: false, Reason: "no authorized candidate had a score"}
	}
	ordered := append([]Candidate(nil), allowed...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := byID[ordered[i].ID], byID[ordered[j].ID]
		if left == right {
			// A stable tie-break keeps pagination consistent.
			return ordered[i].Rank < ordered[j].Rank
		}
		return left > right
	})
	_ = denied
	return RerankResult{Candidates: ordered, Applied: true, Reason: "ranked by shared-rubric score"}
}

// RerankCacheKey includes every input that changes the ranking. A private query
// is hashed by the caller before it reaches a log; this key is for internal
// cache use only.
func RerankCacheKey(normalizedQuery string, filterHash string, candidateRevision int64, contentRevision int64, specID, model string) string {
	parts := []string{normalizedQuery, filterHash, itoa(candidateRevision), itoa(contentRevision), specID, model}
	return strings.Join(parts, "|")
}

// --- Taxonomy proposals -----------------------------------------------------

// ProposalEvidence is the observable basis for a proposal. An unknown reason is
// never hard-labelled as out-of-taxonomy.
type ProposalEvidence struct {
	OutOfTaxonomyCount   int      `json:"out_of_taxonomy_count"`
	HumanCorrectionCount int      `json:"human_correction_count"`
	ConfusionPairs       []string `json:"confusion_pairs"`
}

// TaxonomyProposalDraft is a pending proposal. It never mutates the executable
// vocabulary; approval is a separate, versioned step.
type TaxonomyProposalDraft struct {
	ID             string           `json:"id"`
	Dimension      string           `json:"dimension"`
	SuggestedID    string           `json:"suggested_id"`
	SuggestedLabel string           `json:"suggested_label"`
	Evidence       ProposalEvidence `json:"evidence"`
	Impact         []string         `json:"impact"`
	Status         string           `json:"status"`
}

// DraftProposal builds a pending proposal from observable evidence. It refuses
// to propose when the evidence is too weak rather than creating empty labels.
func DraftProposal(id, dimension, suggestedID, suggestedLabel string, evidence ProposalEvidence) (TaxonomyProposalDraft, error) {
	if strings.TrimSpace(suggestedID) == "" || strings.TrimSpace(suggestedLabel) == "" {
		return TaxonomyProposalDraft{}, errors.New("a proposal requires a suggested id and label")
	}
	if evidence.OutOfTaxonomyCount+evidence.HumanCorrectionCount < 3 {
		return TaxonomyProposalDraft{}, errors.New("insufficient evidence for a taxonomy proposal")
	}
	return TaxonomyProposalDraft{
		ID: id, Dimension: dimension, SuggestedID: suggestedID, SuggestedLabel: suggestedLabel,
		Evidence: evidence, Status: "pending",
	}, nil
}

// ApprovalOutcome distinguishes a display-only change from a semantic one. A
// display change re-renders; a definition change requires a controlled
// re-evaluation.
type ApprovalOutcome struct {
	Approved             bool     `json:"approved"`
	RequiresReevaluation bool     `json:"requires_reevaluation"`
	ImpactScope          []string `json:"impact_scope"`
}

// ApproveProposal validates the approval and reports the required follow-up.
// The proposal must be pending, and a definition change is never display-only.
func ApproveProposal(draft TaxonomyProposalDraft, displayOnly bool) (ApprovalOutcome, error) {
	if draft.Status != "pending" {
		return ApprovalOutcome{}, errors.New("only a pending proposal can be approved")
	}
	if !displayOnly && draft.SuggestedID == "" {
		return ApprovalOutcome{}, errors.New("a semantic change requires a stable id")
	}
	return ApprovalOutcome{
		Approved:             true,
		RequiresReevaluation: !displayOnly,
		ImpactScope:          draft.Impact,
	}, nil
}
