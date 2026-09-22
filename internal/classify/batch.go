package classify

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// Batch is one dependency wave of questions. Every question in a batch is
// independent of the others, so the provider can answer them in one request;
// a question whose material depends on an earlier answer goes into a later
// batch and therefore a later request (B04-T06).
type Batch struct {
	Depth     int        `json:"depth"`
	Questions []Question `json:"questions"`
}

// PlanBatches groups the compiled questions into deterministic dependency
// waves. The order is stable: a batch contains the questions whose DependsOn
// entries are all resolved by earlier batches, sorted by ID.
func PlanBatches(spec QuestionSpec) ([]Batch, error) {
	byID := map[string]Question{}
	for _, question := range spec.Questions {
		if _, exists := byID[question.ID]; exists {
			return nil, fmt.Errorf("duplicate question id %s", question.ID)
		}
		byID[question.ID] = question
	}
	for _, question := range spec.Questions {
		for _, dependency := range question.DependsOn {
			if _, exists := byID[dependency]; !exists {
				return nil, fmt.Errorf("question %s depends on unknown question %s", question.ID, dependency)
			}
			if dependency == question.ID {
				return nil, fmt.Errorf("question %s depends on itself", question.ID)
			}
		}
	}
	resolved := map[string]bool{}
	remaining := make([]Question, len(spec.Questions))
	copy(remaining, spec.Questions)
	batches := []Batch{}
	for len(remaining) > 0 {
		wave := make([]Question, 0, len(remaining))
		next := make([]Question, 0, len(remaining))
		for _, question := range remaining {
			ready := true
			for _, dependency := range question.DependsOn {
				if !resolved[dependency] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, question)
			} else {
				next = append(next, question)
			}
		}
		if len(wave) == 0 {
			// Every remaining question is waiting on another: a cycle.
			ids := make([]string, 0, len(remaining))
			for _, question := range remaining {
				ids = append(ids, question.ID)
			}
			sort.Strings(ids)
			return nil, fmt.Errorf("dependency cycle among questions %v", ids)
		}
		sort.SliceStable(wave, func(i, j int) bool { return wave[i].ID < wave[j].ID })
		batches = append(batches, Batch{Depth: len(batches), Questions: wave})
		for _, question := range wave {
			resolved[question.ID] = true
		}
		remaining = next
	}
	return batches, nil
}

// RequestChunk is one provider request: a bounded slice of a batch. Chunking is
// deterministic so a retry sends exactly the same questions.
type RequestChunk struct {
	BatchIndex int        `json:"batch_index"`
	Index      int        `json:"index"`
	Total      int        `json:"total"`
	Questions  []Question `json:"questions"`
}

// PlanChunks splits each batch into bounded requests. The safety limit is a
// request size, not a semantic limit: a chunk that fails marks only its own
// questions as missing instead of invalidating the whole run.
func PlanChunks(spec QuestionSpec, maxPerRequest int) ([]RequestChunk, error) {
	if maxPerRequest < 1 {
		return nil, errors.New("max questions per request must be positive")
	}
	batches, err := PlanBatches(spec)
	if err != nil {
		return nil, err
	}
	chunks := []RequestChunk{}
	for batchIndex, batch := range batches {
		total := (len(batch.Questions) + maxPerRequest - 1) / maxPerRequest
		for index := 0; index < total; index++ {
			start := index * maxPerRequest
			end := start + maxPerRequest
			if end > len(batch.Questions) {
				end = len(batch.Questions)
			}
			chunks = append(chunks, RequestChunk{
				BatchIndex: batchIndex, Index: index, Total: total,
				Questions: append([]Question{}, batch.Questions[start:end]...),
			})
		}
	}
	return chunks, nil
}

// DefaultMaxQuestionsPerRequest bounds one provider request. It is a transport
// safety limit; the semantic question set is unchanged.
const DefaultMaxQuestionsPerRequest = 32

// EvaluateBatched evaluates the compiled spec in deterministic, bounded
// requests. A chunk that fails marks only its questions as missing and the run
// is reported as partial coverage; a component-level fault still aborts, and a
// run is never reported complete while a question has no answer (B04-T06).
func (c *Client) EvaluateBatched(ctx context.Context, input Input, maxPerRequest int) (RawJudgments, error) {
	if maxPerRequest <= 0 {
		maxPerRequest = DefaultMaxQuestionsPerRequest
	}
	chunks, err := PlanChunks(c.spec, maxPerRequest)
	if err != nil {
		return RawJudgments{}, err
	}
	if len(chunks) == 1 {
		// The common case is one batch: use the ordinary path so usage and drift
		// handling stay identical.
		return c.Evaluate(ctx, input)
	}
	evidence, err := c.evidenceFor(input)
	if err != nil {
		return RawJudgments{}, err
	}
	evidenceHash, err := hashEvidence(evidence)
	if err != nil {
		return RawJudgments{}, err
	}
	wireState, err := evidence.stateForModel()
	if err != nil {
		return RawJudgments{}, err
	}
	merged := RawJudgments{
		MetadataVersion: 1, WireState: string(wireState),
		SpecID: c.spec.SpecID, SpecHash: c.spec.SemanticHash, TaxonomyVersion: c.spec.TaxonomyVersion,
		RequestedModel: c.model,
		Judgments:      map[string]RawJudgment{}, QuestionHashes: map[string]string{},
		EvidenceHash: evidenceHash, EvidenceCoverage: evidence.Coverage, Truncated: evidence.Truncated,
		BatchSemantics: fmt.Sprintf("chunks-of-%d", maxPerRequest),
	}
	missing := []string{}
	for _, chunk := range chunks {
		if ctx.Err() != nil {
			return merged, ctx.Err()
		}
		fresh, err := c.evaluateQuestions(ctx, input, chunk.Questions, evidenceHash, merged.BatchSemantics)
		merged.Calls = append(merged.Calls, fresh.Calls...)
		merged.Usage = mergeUsage(merged.Usage, fresh.Usage)
		merged.UsageMissing = merged.UsageMissing || fresh.UsageMissing
		if err != nil {
			if enrich.PausesComponent(err) {
				return merged, err
			}
			// The failure is contained to this chunk; its questions stay missing
			// and the coverage reflects that.
			for _, question := range chunk.Questions {
				missing = append(missing, question.ID)
			}
			continue
		}
		// A chunk that resolved to a different model than the first chunk cannot
		// be merged into a same-model run; its questions stay missing and the
		// coverage is partial (R2-13).
		if mergedModel := merged.ResolvedModel; mergedModel != "" && fresh.ResolvedModel != "" && fresh.ResolvedModel != mergedModel {
			for _, question := range chunk.Questions {
				missing = append(missing, question.ID)
			}
			merged.AliasDrift = true
			continue
		}
		for id, hash := range fresh.QuestionHashes {
			merged.QuestionHashes[id] = hash
		}
		for id, judgment := range fresh.Judgments {
			merged.Judgments[id] = judgment
		}
		merged.ResolvedModel = fresh.ResolvedModel
		merged.AliasDrift = merged.AliasDrift || fresh.AliasDrift
	}
	sort.Strings(missing)
	merged.Missing = missing
	merged.Coverage = "complete"
	if len(missing) > 0 || len(merged.Judgments) != len(c.spec.Questions) {
		merged.Coverage = "partial"
	}
	return merged, nil
}
