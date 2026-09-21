package extension

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// Judge is the narrow provider surface the extensions may use. It is satisfied
// by the real Jev client; tests use a local fake. A judge never decides what to
// store: the service validates every answer against its own candidate set.
type Judge interface {
	Judge(ctx context.Context, state any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error)
}

// EntityState is the independent lifecycle of an entity run. completed_empty is
// a successful empty result and is never conflated with not_run or failed.
type EntityState string

// Entity lifecycle states. not_run and failed are distinct from a completed
// empty result.
const (
	EntityNotRun            EntityState = "not_run"
	EntityFailed            EntityState = "failed"
	EntityCompletedEmpty    EntityState = "completed_empty"
	EntityCompletedNonempty EntityState = "completed_nonempty"
	EntityStale             EntityState = "stale"
)

// EntityResult is one bounded entity run over stored evidence.
type EntityResult struct {
	State    EntityState `json:"state"`
	Entities []string    `json:"entities"`
	Reason   string      `json:"reason,omitempty"`
	Calls    int         `json:"calls"`
	Tokens   int         `json:"tokens"`
}

// Service wires the bounded extensions to a judge and a budget. It holds no
// global state; one service serves one process.
type Service struct {
	Flags  Flags
	Budget Budget
	Judge  Judge
}

// NewService creates the extension service. A nil judge leaves the model-backed
// capabilities disabled rather than failing the process.
func NewService(flags Flags, budget Budget, judge Judge) *Service {
	return &Service{Flags: flags, Budget: budget, Judge: judge}
}

// estimatedEntityTokens is a conservative per-question estimate. The real
// tokenizer is not available offline, so the ledger over-counts rather than
// under-counting.
const estimatedEntityTokens = 220

// Entities runs the entity pipeline over stored blocks. It is purely additive:
// a disabled flag, an exhausted budget or a provider failure returns an
// explicit state and never touches the stored classification.
func (s *Service) Entities(ctx context.Context, blocks []Block, storedURLs []string) EntityResult {
	if !s.Flags.Entities || s.Judge == nil {
		return EntityResult{State: EntityNotRun, Entities: []string{}, Reason: "entities disabled"}
	}
	candidates := ExtractCandidates(blocks, storedURLs)
	if len(candidates) == 0 {
		return EntityResult{State: EntityCompletedEmpty, Entities: []string{}}
	}
	ledger := NewLedger(s.Budget)
	// One batched judgment over all candidates: one call, bounded tokens.
	if err := ledger.Reserve(estimatedEntityTokens * len(candidates)); err != nil {
		return EntityResult{State: EntityFailed, Entities: []string{}, Reason: ErrBudgetExhausted.Error()}
	}
	questions := make(map[string]classify.ProviderQuestion, len(candidates))
	byID := make(map[string]SurfaceCandidate, len(candidates))
	for index, candidate := range candidates {
		id := fmt.Sprintf("entity_%d", index)
		byID[id] = candidate
		questions[id] = classify.ProviderQuestion{
			Type: classify.TypeNoul,
			Instructions: "材料是否把 `" + candidate.Surface + "` 作为实质讨论的实体（人名、组织、产品、项目、地点等）？" +
				"只是偶然提及、作为普通词出现或无法确认时判否。",
			Criteria: map[string]string{
				"true":  "该名称是材料中实质讨论的实体之一。",
				"false": "未讨论、仅偶然提及或无法确认。",
			},
		}
	}
	state := map[string]any{"material": blocks, "stored_links": storedURLs}
	answers, err := s.Judge.Judge(ctx, state, questions)
	if err != nil {
		return EntityResult{State: EntityFailed, Entities: []string{}, Reason: boundedReason(err), Calls: ledger.Calls(), Tokens: ledger.Tokens()}
	}
	entities := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for id, answer := range answers {
		candidate, ok := byID[id]
		if !ok {
			// A model answer for an unknown question is a contract error, not a
			// new entity.
			return EntityResult{State: EntityFailed, Entities: []string{}, Reason: "verdict for an unknown candidate"}
		}
		if answer.Type != classify.TypeNoul || answer.Noul == nil || answer.Noul.Noul == nil {
			return EntityResult{State: EntityFailed, Entities: []string{}, Reason: "verdict is not a noul judgment"}
		}
		if *answer.Noul.Noul < 0.8 {
			continue
		}
		// Same normalized surface is one entity; a different name is never
		// merged into it.
		key := strings.ToLower(strings.TrimSpace(candidate.Surface))
		if seen[key] {
			continue
		}
		seen[key] = true
		entities = append(entities, candidate.Surface)
	}
	sort.Strings(entities)
	if len(entities) == 0 {
		return EntityResult{State: EntityCompletedEmpty, Entities: []string{}, Calls: ledger.Calls(), Tokens: ledger.Tokens()}
	}
	return EntityResult{State: EntityCompletedNonempty, Entities: entities, Calls: ledger.Calls(), Tokens: ledger.Tokens()}
}

// GapKind is an observable material gap. A legitimate none or a vocabulary
// boundary never escalates.
type GapKind string

// Observable material gaps. A legitimate none or a vocabulary boundary never
// escalates.
const (
	GapNone          GapKind = ""
	GapExternalLink  GapKind = "external_link"
	GapImageText     GapKind = "image_text"
	GapTruncation    GapKind = "truncation"
	GapMissingSource GapKind = "missing_source"
)

// DetectGap reports whether the stored evidence has a real gap that a bounded
// fetch could close. It is conservative: without a stored external link or an
// explicit truncation it reports no gap.
func DetectGap(blocks []Block, storedURLs []string, truncated bool) GapKind {
	if truncated {
		return GapTruncation
	}
	hasExternal := false
	for _, block := range blocks {
		if block.ID != "" && strings.HasPrefix(block.ID, "external-") {
			hasExternal = true
			break
		}
	}
	if !hasExternal {
		for _, value := range storedURLs {
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				return GapExternalLink
			}
		}
	}
	return GapNone
}

// FetchOutcome is the result of one controlled evidence escalation.
type FetchOutcome struct {
	State     string `json:"state"` // completed | blocked | failed
	Reason    string `json:"reason,omitempty"`
	URL       string `json:"url,omitempty"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// MaxEscalatedRunes bounds the text appended from one external fetch so a huge
// page cannot crowd out the primary evidence.
const MaxEscalatedRunes = 20000

// RequestEvidence performs one bounded, allowlisted fetch. It never touches the
// stored source: the caller appends the returned block as a new revision and
// keeps the old content readable on failure.
func (s *Service) RequestEvidence(ctx context.Context, client *http.Client, policy FetchPolicy, raw string) FetchOutcome {
	if !s.Flags.Evidence {
		return FetchOutcome{State: "blocked", Reason: "evidence escalation disabled"}
	}
	content, err := Fetch(ctx, client, policy, raw)
	if err != nil {
		if errors.Is(err, ErrFetchBlocked) {
			return FetchOutcome{State: "blocked", Reason: boundedReason(err)}
		}
		return FetchOutcome{State: "failed", Reason: boundedReason(err)}
	}
	text := content.Text
	truncated := content.Truncated
	if runes := []rune(text); len(runes) > MaxEscalatedRunes {
		text = string(runes[:MaxEscalatedRunes])
		truncated = true
	}
	if strings.TrimSpace(text) == "" {
		return FetchOutcome{State: "failed", Reason: "fetched content had no readable text"}
	}
	return FetchOutcome{State: "completed", URL: content.URL, Text: text, Truncated: truncated}
}

// RerankCandidates asks the model for one comparable score per authorized
// candidate on a shared rubric. Any failure or an exhausted budget returns the
// original order with an explicit reason (B09-T09/T10).
func (s *Service) RerankCandidates(ctx context.Context, query string, candidates []Candidate) RerankResult {
	if !s.Flags.Rerank || s.Judge == nil {
		return Rerank(candidates, nil, false)
	}
	authorized := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Allowed {
			authorized = append(authorized, candidate)
		}
	}
	if len(authorized) == 0 || len(authorized) > 20 {
		return Rerank(candidates, nil, false)
	}
	ledger := NewLedger(s.Budget)
	if err := ledger.Reserve(estimatedEntityTokens * len(authorized)); err != nil {
		return Rerank(candidates, nil, false)
	}
	questions := make(map[string]classify.ProviderQuestion, len(authorized))
	byID := make(map[string]Candidate, len(authorized))
	for index, candidate := range authorized {
		id := fmt.Sprintf("rerank_%d", index)
		byID[id] = candidate
		questions[id] = classify.ProviderQuestion{
			Type: classify.TypeScore,
			Instructions: "在同一个标准下，以下材料与查询 `" + query + "` 的相关程度如何？" +
				"只比较材料本身，不因为标题或来源不同而加减分。",
			Criteria: []string{"不相关", "略有关系", "明显相关", "高度相关"},
		}
	}
	state := map[string]any{
		"query": query,
		"candidates": func() []map[string]string {
			out := make([]map[string]string, 0, len(authorized))
			for id, candidate := range byID {
				out = append(out, map[string]string{"id": id, "text": candidate.Text})
			}
			sort.Slice(out, func(i, j int) bool { return out[i]["id"] < out[j]["id"] })
			return out
		}(),
	}
	answers, err := s.Judge.Judge(ctx, state, questions)
	if err != nil {
		return Rerank(candidates, nil, false)
	}
	scores := make([]RerankScore, 0, len(answers))
	for id, answer := range answers {
		candidate, ok := byID[id]
		if !ok || answer.Type != classify.TypeScore || answer.Score == nil {
			return Rerank(candidates, nil, false)
		}
		scores = append(scores, RerankScore{ID: candidate.ID, Score: int(answer.Score.Score*100 + 0.5)})
	}
	return Rerank(candidates, scores, true)
}

func boundedReason(err error) string {
	message := err.Error()
	if len(message) > 300 {
		return message[:300]
	}
	return message
}
