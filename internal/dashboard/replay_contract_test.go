package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type replayContractBackend struct {
	Backend
	V2Backend
	run     cairn.StoredRun
	spec    cairn.StoredQuestionSpec
	writes  []cairn.PolicyReplayRequest
	retried int
}

func (b *replayContractBackend) GetReplayableRun(context.Context, int64) (*cairn.StoredRun, error) {
	return &b.run, nil
}
func (b *replayContractBackend) GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error) {
	return b.spec, nil
}
func (b *replayContractBackend) GetV2Selection(context.Context, int64) (cairn.V2SelectionView, error) {
	return cairn.V2SelectionView{Revision: 41}, nil
}
func (b *replayContractBackend) Handshake(context.Context, cairn.Capabilities) (cairn.HandshakeResult, error) {
	return cairn.HandshakeResult{Target: cairn.ClassificationTarget{Generation: 2}}, nil
}
func (b *replayContractBackend) SubmitPolicyReplay(_ context.Context, _ int64, p cairn.PolicyReplayRequest) (json.RawMessage, error) {
	b.writes = append(b.writes, p)
	return json.RawMessage(`{"decision_id":1}`), nil
}
func (b *replayContractBackend) RetryClassification(context.Context, int64) error {
	b.retried++
	return nil
}

func replayContractFixture(t *testing.T) *replayContractBackend {
	t.Helper()
	catalog := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "topic", Label: "Topic", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "reference", Label: "Reference", Active: true}}}
	spec, err := classify.CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]any{}
	for _, q := range spec.Questions {
		if q.Kind == classify.QuestionNoul {
			answers[q.ID] = map[string]any{"type": "noul", "noul": .72}
		} else {
			probs := map[string]float64{}
			for _, option := range q.AnswerOptions() {
				probs[option] = 0
			}
			probs["none"] = 1
			answers[q.ID] = map[string]any{"type": "choice", "choice": "none", "probabilities": probs}
		}
	}
	encoded, _ := json.Marshal(answers)
	policy := classify.DefaultPolicy()
	policy.Version = "jev-policy-v2"
	policy.BlockPersonalUse = false
	policy.PreferSpecificTopics = false
	policy.MinPrimaryTags, policy.MaxPrimaryTags, policy.FunctionSupportAccept = 0, 0, 0
	policyJSON, _ := json.Marshal(policy)
	payload, err := classify.MarshalSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &replayContractBackend{run: cairn.StoredRun{ID: 9, SpecID: spec.SpecID, SpecHash: spec.SemanticHash, Answers: encoded, Policy: policyJSON, RequestedModel: "jev-1.13.0", ResolvedModel: "jev-1.13.0", ContentRevision: 3, Coverage: "complete", Status: "succeeded", TargetGeneration: 1}, spec: cairn.StoredQuestionSpec{Payload: payload}}
}

func TestDashboardPolicyReplayUsesHashIdentityAndCASWithoutLegacyWrite(t *testing.T) {
	b := replayContractFixture(t)
	s := &Server{backend: b, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "1")
	for _, body := range []string{`{"topic_accept":0.7}`, `{"topic_accept":0.7,"commit":true}`, `{"topic_accept":0.75,"commit":true}`} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", "4")
		rec := httptest.NewRecorder()
		s.replayPolicy(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("replay contract: %d %s", rec.Code, rec.Body.String())
		}
	}
	if len(b.writes) != 2 {
		t.Fatal("dry-run wrote a decision")
	}
	for _, p := range b.writes {
		if p.ExpectedRevision != 41 || p.ExpectedTargetGeneration != 2 || p.ContentRevision != 3 || p.RunIDs[0] != 9 || p.PolicyHash == "" {
			t.Fatalf("missing controlled CAS/input identity: %+v", p)
		}
	}
	if b.writes[0].OperationKey == b.writes[1].OperationKey || b.writes[0].PolicyVersion == b.writes[1].PolicyVersion {
		t.Fatal("different UI policies collided")
	}
}

func TestClassificationRetryWakesOnlyClassificationLane(t *testing.T) {
	b := &replayContractBackend{}
	source, classification := make(chan struct{}, 1), make(chan struct{}, 1)
	s := &Server{backend: b, wakeup: source, classificationWakeup: classification}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{}`))
	req.SetPathValue("id", "4")
	rec := httptest.NewRecorder()
	s.retryClassification(rec, req)
	if rec.Code != 200 || b.retried != 1 || len(classification) != 1 || len(source) != 0 {
		t.Fatalf("retry woke wrong lane: code=%d source=%d classification=%d", rec.Code, len(source), len(classification))
	}
}
