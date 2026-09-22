package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

// rerankJudge scores by the candidate id embedded in the question instructions,
// so the test is independent of map iteration order (R2-11).
type rerankJudge struct {
	requests *[]map[string]classify.ProviderQuestion
}

func (judge rerankJudge) Judge(_ context.Context, _ any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	if judge.requests != nil {
		*judge.requests = append(*judge.requests, questions)
	}
	answers := map[string]classify.RawAnswer{}
	for id, question := range questions {
		value := 0.0
		// The second candidate carries the higher relevance in its own text.
		if strings.Contains(instructionText(question.Instructions), "高相关材料") {
			value = 3
		}
		answers[id] = classify.RawAnswer{Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: value}}
	}
	return answers, nil
}

func instructionText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

// TestRerankEndpointKeepsCandidatesAndFallsBackExplicitly covers B09-T09/T10 at
// the HTTP boundary: the endpoint only ever reorders the authorized list, and a
// disabled extension reports that instead of pretending to rank.
func TestRerankEndpointIsBoundedAndFallsBackExplicitly(t *testing.T) {
	backend := &fakeBackend{page: cairn.BookmarkPage{Items: []cairn.Bookmark{
		{ID: 1, AITitle: "低相关材料", Summary: "无关内容"},
		{ID: 2, AITitle: "高相关材料", Summary: "LLM 评估方法"},
	}}}
	server := New(context.Background(), nil, backend, nil, testLogger(), 1)
	handler := server.Handler()

	// Disabled by default.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/rerank", `{"query":"llm","limit":5}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var disabled struct {
		Applied bool   `json:"applied"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.Applied || !strings.Contains(disabled.Reason, "disabled") {
		t.Fatalf("disabled rerank must report itself: %+v", disabled)
	}

	flags := extension.DefaultFlags()
	flags.Rerank = true
	var captured []map[string]classify.ProviderQuestion
	server.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), rerankJudge{requests: &captured}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/rerank", `{"query":"llm","limit":5}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var ranked extension.RerankResult
	if err := json.Unmarshal(response.Body.Bytes(), &ranked); err != nil {
		t.Fatal(err)
	}
	if !ranked.Applied || len(ranked.Candidates) != 2 {
		t.Fatalf("rerank result = %+v", ranked)
	}
	if ranked.Candidates[0].ID != "2" {
		t.Fatalf("the higher-scored candidate must rank first: %+v", ranked.Candidates)
	}
	// Every question must name exactly its own candidate; a shared question with
	// no target would let the model rate the wrong material (R2-11).
	if len(captured) != 1 || len(captured[0]) != 2 {
		t.Fatalf("captured rerank questions = %+v", captured)
	}
	for id, question := range captured[0] {
		text := instructionText(question.Instructions)
		candidateID := strings.TrimPrefix(id, "rerank_")
		if candidateID == id {
			t.Fatalf("question key %q is not bound to a candidate", id)
		}
		if !strings.Contains(text, "材料 `"+candidateID+"`") {
			t.Fatalf("question %s does not bind its candidate: %s", id, text)
		}
	}
	// The second call is stable; further HTTP requests must fall back once
	// either candidate reaches the shared per-item allowance (B09-T01/T10).
	for run := 0; run < 5; run++ {
		repeat := httptest.NewRecorder()
		handler.ServeHTTP(repeat, jsonRequest(http.MethodPost, "/api/rerank", `{"query":"llm","limit":5}`))
		var again extension.RerankResult
		if err := json.Unmarshal(repeat.Body.Bytes(), &again); err != nil {
			t.Fatal(err)
		}
		if run == 0 && (!again.Applied || again.Candidates[0].ID != "2") {
			t.Fatalf("run %d ordered %+v", run, again.Candidates)
		}
		if run > 0 && (again.Applied || again.Candidates[0].ID != "1" || !strings.Contains(again.Reason, "budget")) {
			t.Fatalf("run %d did not preserve original order on exhaustion: %+v", run, again)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("HTTP retries escaped budget: %d judgments", len(captured))
	}
	// A candidate outside the authorized set is never returned.
	for _, candidate := range ranked.Candidates {
		if candidate.ID != "1" && candidate.ID != "2" {
			t.Fatalf("rerank invented a candidate: %+v", candidate)
		}
	}
}

func jsonRequest(method, path, body string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}
