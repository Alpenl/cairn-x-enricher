package extension

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// EntityBinding identifies the immutable objective material, not human edits.
type EntityBinding struct {
	LinkID             int64  `json:"link_id"`
	EvidenceSnapshotID int64  `json:"evidence_snapshot_id"`
	ContentRevision    int64  `json:"content_revision"`
	ContentHash        string `json:"content_hash"`
}

// EntityClaim contains private evidence and exact provider request bytes.
type EntityClaim struct {
	EntityBinding
	OwnerToken  string             `json:"owner_token"`
	RequestJSON string             `json:"request_json"`
	Candidates  []SurfaceCandidate `json:"candidates"`
	SpecHash    string             `json:"spec_hash"`
}

// EntityReceipt cannot by itself authorize a paid request; budget is separate.
type EntityReceipt = RerankReceipt

// EntityCompletion stores the original owner's result without repeating inference.
type EntityCompletion = RerankCompletion

// EntityStore persists ownership and raw judgments across processes.
type EntityStore interface {
	ClaimEntity(context.Context, EntityClaim) (EntityReceipt, error)
	CompleteEntity(context.Context, string, EntityCompletion) (EntityReceipt, error)
	GetEntity(context.Context, string) (EntityReceipt, error)
}

// SetEntityStore is startup-only. Production requires this protocol.
func (s *Service) SetEntityStore(store EntityStore) { s.entityStore = store }

// EntitiesForSnapshot runs the same pipeline with its canonical source binding.
func (s *Service) EntitiesForSnapshot(ctx context.Context, binding EntityBinding, blocks []Block, storedURLs []string) EntityResult {
	return s.entities(ctx, strconv.FormatInt(binding.LinkID, 10), blocks, storedURLs, &binding)
}

func validateEntityAnswers(questions map[string]classify.ProviderQuestion, answers map[string]classify.RawAnswer) error {
	for id := range answers {
		if _, ok := questions[id]; !ok {
			return errors.New("verdict for an unknown candidate")
		}
	}
	if len(answers) != len(questions) {
		return errors.New("incomplete entity answer set")
	}
	for id := range questions {
		a, ok := answers[id]
		if !ok || a.Type != classify.TypeNoul || a.Noul == nil || a.Noul.Noul == nil {
			return errors.New("invalid entity answer")
		}
		p := *a.Noul.Noul
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return errors.New("invalid entity probability")
		}
	}
	return nil
}

func (s *Service) cachedEntities(ctx context.Context, binding *EntityBinding, candidates []SurfaceCandidate, state any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, int, int, string, error) {
	if binding == nil || binding.LinkID < 1 || binding.EvidenceSnapshotID < 1 || binding.ContentRevision < 1 || len(binding.ContentHash) != 64 {
		return nil, 0, 0, "", errors.New("entity cache requires bound evidence")
	}
	preparer, ok := s.Judge.(preparedJudge)
	if !ok {
		return nil, 0, 0, "", errors.New("entity provider cannot prepare an auditable request")
	}
	wire, err := preparer.PreparedJudgeRequest(state, questions)
	if err != nil {
		return nil, 0, 0, "", err
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return nil, 0, 0, "", err
	}
	owner := hex.EncodeToString(nonce)
	// The full questions and candidates are in identity too. Version the local
	// selection semantics explicitly; changing thresholds cannot reuse a receipt.
	spec, _ := json.Marshal(map[string]any{"extractor": "surface-spans-v1", "accept": 0.8, "normalization": "lowercase-trim-v1", "questions": questions})
	ctx, stop := context.WithTimeout(ctx, s.Budget.Timeout)
	defer stop()
	receipt, err := s.entityStore.ClaimEntity(ctx, EntityClaim{EntityBinding: *binding, OwnerToken: owner, RequestJSON: string(wire), Candidates: candidates, SpecHash: digestBytes(spec)})
	if err != nil {
		return nil, 0, 0, "", errors.New("entity cache unavailable or material changed")
	}
	operation := func(r EntityReceipt) string {
		raw, _ := json.Marshal(r.Answers)
		return "entity-cache-" + r.Key + "-" + digestBytes(raw)
	}
	if receipt.Status == "completed" {
		if err := validateEntityAnswers(questions, receipt.Answers); err != nil {
			return nil, 0, 0, "", err
		}
		return receipt.Answers, 0, 0, operation(receipt), nil
	}
	if receipt.Status != "pending" || !receipt.Owned {
		return nil, 0, 0, "", errors.New("entity result pending or previously failed")
	}
	answers, calls, tokens, judgeErr := s.judgeBounded(ctx, "entity", []string{strconv.FormatInt(binding.LinkID, 10)}, state, questions)
	if judgeErr == nil {
		judgeErr = validateEntityAnswers(questions, answers)
	}
	completion := EntityCompletion{OwnerToken: owner, Status: "completed", Answers: answers}
	if judgeErr != nil {
		completion.Status = "failed"
		completion.Answers = map[string]classify.RawAnswer{}
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var saved EntityReceipt
	expected, _ := json.Marshal(completion.Answers)
	matches := func(r EntityReceipt) bool {
		raw, _ := json.Marshal(r.Answers)
		return r.Key == receipt.Key && r.Status == completion.Status && string(raw) == string(expected)
	}
	for attempt := 0; attempt < 2; attempt++ {
		saved, err = s.entityStore.CompleteEntity(saveCtx, receipt.Key, completion)
		if err == nil && matches(saved) {
			break
		}
		recovered, readErr := s.entityStore.GetEntity(saveCtx, receipt.Key)
		if readErr == nil && matches(recovered) {
			saved, err = recovered, nil
			break
		}
		err = errors.New("entity result not durably confirmed")
		if saveCtx.Err() != nil {
			break
		}
	}
	if judgeErr != nil {
		return nil, calls, tokens, "", judgeErr
	}
	if err != nil || !matches(saved) {
		return nil, calls, tokens, "", errors.New("entity result not durably confirmed")
	}
	return saved.Answers, calls, tokens, operation(saved), nil
}
