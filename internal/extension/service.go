package extension

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

// Service shares a process-local ledger and a durable Worker budget across all
// extension entry points. Configure it before starting concurrent processing.
type Service struct {
	Flags       Flags
	Budget      Budget
	Judge       Judge
	ledger      *Ledger
	store       BudgetStore
	rerankStore RerankStore
}

// NewService creates the extension service. A nil judge leaves the model-backed
// capabilities disabled rather than failing the process.
func NewService(flags Flags, budget Budget, judge Judge) *Service {
	return &Service{Flags: flags, Budget: budget, Judge: judge, ledger: NewLedger(budget)}
}

// Entities runs the entity pipeline over stored blocks. It is purely additive:
// a disabled flag, an exhausted budget or a provider failure returns an
// explicit state and never touches the stored classification.
func (s *Service) Entities(ctx context.Context, blocks []Block, storedURLs []string) EntityResult {
	return s.entities(ctx, "unscoped", blocks, storedURLs)
}

// EntitiesForItem binds the production budget to the owning bookmark.
func (s *Service) EntitiesForItem(ctx context.Context, id int64, blocks []Block, storedURLs []string) EntityResult {
	return s.entities(ctx, strconv.FormatInt(id, 10), blocks, storedURLs)
}

func (s *Service) entities(ctx context.Context, item string, blocks []Block, storedURLs []string) EntityResult {
	if !s.Flags.Entities || s.Judge == nil {
		return EntityResult{State: EntityNotRun, Entities: []string{}, Reason: "entities disabled"}
	}
	candidates := ExtractCandidates(blocks, storedURLs)
	if len(candidates) == 0 {
		return EntityResult{State: EntityCompletedEmpty, Entities: []string{}}
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
	answers, calls, tokens, err := s.judgeBounded(ctx, "entity", []string{item}, state, questions)
	if err != nil {
		return EntityResult{State: EntityFailed, Entities: []string{}, Reason: boundedReason(err), Calls: calls, Tokens: tokens}
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
		return EntityResult{State: EntityCompletedEmpty, Entities: []string{}, Calls: calls, Tokens: tokens}
	}
	return EntityResult{State: EntityCompletedNonempty, Entities: entities, Calls: calls, Tokens: tokens}
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
	for _, value := range storedURLs {
		if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			continue
		}
		found := false
		for _, block := range blocks {
			if block.Role == "external_article" && block.URL == value {
				found = true
				break
			}
		}
		if !found {
			return GapExternalLink
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
	Truncated bool   `json:"truncated"`
}

// MaxEscalatedRunes bounds the text appended from one external fetch so a huge
// page cannot crowd out the primary evidence.
const MaxEscalatedRunes = 20000

// RequestEvidence performs one bounded, allowlisted fetch. It never touches the
// stored source: the caller appends the returned block as a new revision and
// keeps the old content readable on failure.
func (s *Service) RequestEvidence(ctx context.Context, client *http.Client, policy FetchPolicy, raw string) FetchOutcome {
	return s.requestEvidence(ctx, "unscoped", client, policy, raw)
}

// RequestEvidenceForItem also charges the shared cross-process call limit.
func (s *Service) RequestEvidenceForItem(ctx context.Context, id int64, client *http.Client, policy FetchPolicy, raw string) FetchOutcome {
	return s.requestEvidence(ctx, strconv.FormatInt(id, 10), client, policy, raw)
}

func (s *Service) requestEvidence(ctx context.Context, item string, client *http.Client, policy FetchPolicy, raw string) FetchOutcome {
	if !s.Flags.Evidence {
		return FetchOutcome{State: "blocked", Reason: "evidence escalation disabled"}
	}
	if s.Budget.Timeout <= 0 {
		return FetchOutcome{State: "blocked", Reason: "invalid extension timeout"}
	}
	callCtx, cancel := context.WithTimeout(ctx, s.Budget.Timeout)
	defer cancel()
	if err := s.reserve(callCtx, "evidence", []string{item}, 0); err != nil {
		return FetchOutcome{State: "blocked", Reason: boundedReason(err)}
	}
	content, err := Fetch(callCtx, client, policy, raw)
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
	return s.RerankCandidatesScoped(ctx, query, candidates, digestBytes([]byte("unfiltered-current-candidates")))
}

const rerankInstructionTemplate = "在同一个标准下，材料 `%s` 与查询 `%s` 的相关程度如何？被评价材料：`%s`。只评价这一条材料本身，不因为标题、来源或其他候选而加减分。"

var rerankCriteria = []string{"不相关", "略有关系", "明显相关", "高度相关"}

// RerankCandidatesScoped binds the complete filter/page scope into cache identity.
// It only ranks supplied candidates, never across pages.
func (s *Service) RerankCandidatesScoped(ctx context.Context, query string, candidates []Candidate, scopeHash string) RerankResult {
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
	// Every question names exactly one candidate in its own instructions, so the
	// model always knows which material it is rating. The shared rubric stays
	// constant; the evaluation target does not (R2-11).
	questions := make(map[string]classify.ProviderQuestion, len(authorized))
	for _, candidate := range authorized {
		id := "rerank_" + candidate.ID
		text := candidate.Text
		if runes := []rune(text); len(runes) > 600 {
			text = string(runes[:600])
		}
		questions[id] = classify.ProviderQuestion{
			Type:         classify.TypeScore,
			Instructions: fmt.Sprintf(rerankInstructionTemplate, candidate.ID, query, text),
			Criteria:     rerankCriteria,
		}
	}
	state := map[string]any{"query": query}
	items := make([]string, 0, len(authorized))
	for _, candidate := range authorized {
		items = append(items, candidate.ID)
	}
	var answers map[string]classify.RawAnswer
	var err error
	cacheStatus := ""
	if s.rerankStore != nil {
		answers, cacheStatus, err = s.cachedRerank(ctx, scopeHash, authorized, state, questions)
	} else {
		answers, _, _, err = s.judgeBounded(ctx, "rerank", items, state, questions)
	}
	if err != nil {
		result := Rerank(candidates, nil, false)
		result.Reason = "rerank unavailable: " + boundedReason(err)
		result.CacheStatus = cacheStatus
		return result
	}
	scores, err := rerankScores(authorized, answers)
	if err != nil {
		result := Rerank(candidates, nil, false)
		result.Reason = "invalid rerank result"
		return result
	}
	result := Rerank(candidates, scores, true)
	result.CacheStatus = cacheStatus
	return result
}

func boundedReason(err error) string {
	message := err.Error()
	if len(message) > 300 {
		return message[:300]
	}
	return message
}
