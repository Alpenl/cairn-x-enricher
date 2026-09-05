package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResponsesClientForcesXSearchAndParsesStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer model-key" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Idempotency-Key") != "cairn-link-42-attempt-3" {
			t.Errorf("Idempotency-Key = %q", request.Header.Get("Idempotency-Key"))
		}

		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "grok-4.6" || body["tool_choice"] != "required" {
			t.Errorf("request body = %#v", body)
		}
		messages := body["input"].([]any)
		content := messages[0].(map[string]any)["content"].(string)
		for _, required := range []string{"https://x.com/user/status/42", "约20个简体中文字符", "原始语言", "完整简体中文译文", "pbs.twimg.com/media", "忽略广告和无关项"} {
			if !strings.Contains(content, required) {
				t.Errorf("prompt does not contain %q: %q", required, content)
			}
		}
		if strings.Contains(content, "later") {
			t.Errorf("prompt = %q", content)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "status":"completed",
          "model":"grok-4.6-20260901",
          "output":[
            {"type":"reasoning","status":"completed"},
            {"type":"custom_tool_call","name":"x_thread_fetch","status":"completed"},
            {"type":"message","status":"completed","content":[
              {"type":"output_text","text":"{\"ai_title\":\"人工智能生成的测试中文标题\",\"original_language\":\"en\",\"original_text\":\"source\",\"translated_text\":\"中文译文\",\"summary\":\"summary\",\"related_links\":[\"https://example.com/a\"],\"image_urls\":[\"https://pbs.twimg.com/media/abc?format=jpg&name=large\"]}"}
            ]}
          ]
        }`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL+"/v1", "model-key", "grok-4.6", 8192, "test-agent", server.Client())
	candidate, err := client.Generate(context.Background(), Input{
		ID: 42, URL: "https://x.com/user/status/42", Note: "later", Attempt: 3,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !candidate.SearchVerified || candidate.Result.AITitle != "人工智能生成的测试中文标题" || candidate.Result.OriginalText != "source" || candidate.Result.TranslatedText != "中文译文" || len(candidate.Result.ImageURLs) != 1 || candidate.Result.Model != "grok-4.6-20260901" {
		t.Fatalf("Generate() = %+v", candidate)
	}
}

func TestResponsesClientRejectsMalformedStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "status":"completed",
          "model":"grok-test",
          "output":[
            {"type":"custom_tool_call","name":"x_thread_fetch","status":"completed"},
            {"type":"message","status":"completed","content":[{"type":"output_text","text":"{\"ai_title\":\"人工智能生成的测试中文标题\",\"original_language\":\"en\",\"original_text\":\"text\",\"translated_text\":\"译文\",\"summary\":\"summary\",\"related_links\":[],\"image_urls\":[],\"extra\":true}"}]}
          ]
        }`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client())
	if _, err := client.Generate(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1}); err == nil {
		t.Fatal("Generate() error = nil, want strict JSON error")
	}
}

func TestResponsesClientReturnsSanitizedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "0")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "secret-key", "model", 1024, "", server.Client())
	_, err := client.Generate(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	var httpErr *ModelHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Generate() error = %T %v", err, err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("error contains API key")
	}
}

func TestResponsesClientRetriesTransientHTTPFailures(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"error":{"message":"Upstream service temporarily unavailable"}}`))
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "status":"completed",
          "model":"grok-test",
          "output":[
            {"type":"x_search_call","status":"completed"},
            {"type":"message","status":"completed","content":[
              {"type":"output_text","text":"{\"ai_title\":\"人工智能生成的测试中文标题\",\"original_language\":\"en\",\"original_text\":\"source\",\"translated_text\":\"中文译文\",\"summary\":\"summary\",\"related_links\":[],\"image_urls\":[]}"}
            ]}
          ]
        }`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client())
	candidate, err := client.Generate(context.Background(), Input{ID: 9, URL: "https://x.com/a/status/9", Attempt: 5})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if candidate.Result.AITitle != "人工智能生成的测试中文标题" {
		t.Fatalf("Generate() = %+v", candidate)
	}
}

func TestResponsesClientFallsBackToPostOnlyPromptAfterTransientHTTPFailures(t *testing.T) {
	var sawFallback bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Input []struct {
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Input) != 1 {
			t.Fatalf("input = %+v", body.Input)
		}
		if !strings.Contains(body.Input[0].Content, "不要展开全量评论") {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"error":{"message":"Upstream service temporarily unavailable"}}`))
			return
		}
		sawFallback = true
		if !strings.Contains(request.Header.Get("Idempotency-Key"), "-post-1") {
			t.Fatalf("fallback Idempotency-Key = %q", request.Header.Get("Idempotency-Key"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "status":"completed",
          "model":"grok-test",
          "output":[
            {"type":"x_search_call","status":"completed"},
            {"type":"message","status":"completed","content":[
              {"type":"output_text","text":"{\"ai_title\":\"公众号三年经验与创作工作流\",\"original_language\":\"zh\",\"original_text\":\"source\",\"translated_text\":\"中文译文\",\"summary\":\"summary\",\"related_links\":[],\"image_urls\":[]}"}
            ]}
          ]
        }`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client())
	candidate, err := client.Generate(context.Background(), Input{ID: 12, URL: "https://x.com/a/status/12", Attempt: 5})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !sawFallback {
		t.Fatal("fallback prompt was not used")
	}
	if candidate.Result.AITitle != "公众号三年经验与创作工作流" {
		t.Fatalf("Generate() = %+v", candidate)
	}
}

func TestResponsesClientTransformsTrustedSourceWithoutXSearch(t *testing.T) {
	sourceText := "这是人工粘贴的原帖原文，包含完整内容。"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Idempotency-Key") != "cairn-link-20-attempt-6-source-1" {
			t.Fatalf("Idempotency-Key = %q", request.Header.Get("Idempotency-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := body["tools"]; exists {
			t.Fatalf("trusted source request includes tools: %#v", body["tools"])
		}
		messages := body["input"].([]any)
		content := messages[0].(map[string]any)["content"].(string)
		for _, required := range []string{"不要搜索", "原文:", sourceText} {
			if !strings.Contains(content, required) {
				t.Fatalf("source prompt does not contain %q: %q", required, content)
			}
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "status":"completed",
          "model":"grok-test",
          "output":[
            {"type":"message","status":"completed","content":[
              {"type":"output_text","text":"{\"ai_title\":\"人工原文生成标题\",\"original_language\":\"zh\",\"original_text\":\"model copy\",\"translated_text\":\"这是人工粘贴的原帖原文，包含完整内容。\",\"summary\":\"summary\",\"related_links\":[],\"image_urls\":[\"https://pbs.twimg.com/media/ignored?format=jpg\"]}"}
            ]}
          ]
        }`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client())
	candidate, err := client.Generate(context.Background(), Input{
		ID: 20, URL: "https://x.com/a/status/20", Attempt: 6,
		SourceText:   sourceText,
		RelatedLinks: []string{"https://example.com/existing"},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !candidate.SearchVerified {
		t.Fatal("trusted source candidate was not marked verified")
	}
	if candidate.Result.OriginalText != sourceText {
		t.Fatalf("OriginalText = %q", candidate.Result.OriginalText)
	}
	if len(candidate.Result.RelatedLinks) != 1 || candidate.Result.RelatedLinks[0] != "https://example.com/existing" {
		t.Fatalf("RelatedLinks = %#v", candidate.Result.RelatedLinks)
	}
	if len(candidate.Result.ImageURLs) != 0 {
		t.Fatalf("ImageURLs = %#v", candidate.Result.ImageURLs)
	}
}

func TestSlowModelFailuresSkipSamePromptRetry(t *testing.T) {
	if shouldRetryModelRequest(http.StatusBadGateway, 1, slowModelFailure) {
		t.Fatal("slow model failure should move to fallback instead of retrying the same prompt")
	}
	if !shouldRetryModelRequest(http.StatusBadGateway, 1, time.Second) {
		t.Fatal("fast transient model failure should retry")
	}
}
