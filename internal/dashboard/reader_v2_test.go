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

type rerankJudge struct{}

func (rerankJudge) Judge(_ context.Context, _ any, questions map[string]classify.ProviderQuestion) (map[string]classify.RawAnswer, error) {
	answers := map[string]classify.RawAnswer{}
	// The second candidate is the more relevant one.
	index := 0
	for id := range questions {
		value := 0.0
		if index == 1 {
			value = 3
		}
		answers[id] = classify.RawAnswer{Type: classify.TypeScore, Score: &classify.ScoreAnswer{Score: value}}
		index++
	}
	return answers, nil
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
	server.SetExtensions(extension.NewService(flags, extension.DefaultBudget(), rerankJudge{}))
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
