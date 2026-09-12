package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestGenerateDegradesWhenSearchEvidenceIsMissing proves the degradation chain
// also responds to a successful-but-unusable response.
//
// A measured real case: the thread prompt returned HTTP 200 for a short post,
// but the response contained no completed X search, so validation would reject
// it. Returning that candidate unexamined abandoned the bookmark. The client
// must instead try the next prompt, which for that bookmark did retrieve.
func TestGenerateDegradesWhenSearchEvidenceIsMissing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		content := body["input"].([]any)[0].(map[string]any)["content"].(string)

		w.Header().Set("Content-Type", "application/json")
		// The first prompt (thread) answers without any search tool call.
		// The second prompt (post-only) completes a search.
		if attempt == 1 && strings.Contains(content, "相关评论") {
			_, _ = w.Write([]byte(`{"status":"completed","model":"grok-4.6","output":[
              {"type":"reasoning","status":"completed"},
              {"type":"message","status":"completed","content":[
                {"type":"output_text","text":"{\"ai_title\":\"没有搜索证据的中文标题\",\"original_language\":\"en\",\"original_text\":\"invented\",\"translated_text\":\"译文\",\"summary\":\"摘要\",\"related_links\":[],\"image_urls\":[],\"classification\":{\"topics\":[],\"form\":\"\",\"use\":\"\",\"why_suggestion\":\"\",\"entities\":[],\"uncertainty\":true}}"}
              ]}
            ]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","model":"grok-4.6","output":[
          {"type":"custom_tool_call","name":"x_thread_fetch","status":"completed"},
          {"type":"message","status":"completed","content":[
            {"type":"output_text","text":"{\"ai_title\":\"降级后成功的中文标题\",\"original_language\":\"en\",\"original_text\":\"retrieved\",\"translated_text\":\"译文\",\"summary\":\"摘要\",\"related_links\":[],\"image_urls\":[],\"classification\":{\"topics\":[],\"form\":\"\",\"use\":\"\",\"why_suggestion\":\"\",\"entities\":[],\"uncertainty\":true}}"}
          ]}
        ]}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "grok-4.6", 1024, "test", server.Client(), testTaxonomy())
	candidate, err := client.Generate(context.Background(), Input{
		ID: 14, URL: "https://x.com/a/status/14", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v, want the post-only prompt to rescue it", err)
	}
	if !candidate.SearchVerified {
		t.Fatal("Generate() returned a candidate without search evidence")
	}
	if candidate.Result.OriginalText != "retrieved" {
		t.Fatalf("OriginalText = %q, want the retrieved text from the second prompt", candidate.Result.OriginalText)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (thread then post-only)", got)
	}
}

// TestGenerateStopsOnNonRetryableError keeps a configuration fault from being
// multiplied across prompts: an auth or quota error must abort immediately.
func TestGenerateStopsOnNonRetryableError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"auth_error"}}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "grok-4.6", 1024, "test", server.Client(), testTaxonomy())
	_, err := client.Generate(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	if err == nil {
		t.Fatal("Generate() error = nil, want the auth failure")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want 1; a non-retryable error must not be retried per prompt", got)
	}
}

// TestGenerateReportsLastErrorWhenAllPromptsFail covers every prompt returning
// a successful response with no search evidence: the error must name the real
// cause rather than a downstream validation detail.
func TestGenerateReportsLastErrorWhenAllPromptsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","model":"grok-4.6","output":[
          {"type":"message","status":"completed","content":[
            {"type":"output_text","text":"{\"ai_title\":\"无证据标题\",\"original_language\":\"en\",\"original_text\":\"x\",\"translated_text\":\"译文\",\"summary\":\"摘要\",\"related_links\":[],\"image_urls\":[],\"classification\":{\"topics\":[],\"form\":\"\",\"use\":\"\",\"why_suggestion\":\"\",\"entities\":[],\"uncertainty\":true}}"}
          ]}
        ]}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "grok-4.6", 1024, "test", server.Client(), testTaxonomy())
	_, err := client.Generate(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	if err == nil {
		t.Fatal("Generate() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("Generate() error = %q, want it to name the missing search evidence", err)
	}
}
