package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		if !strings.Contains(content, "https://x.com/user/status/42") || !strings.Contains(content, "忽略广告和无关链接") {
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
              {"type":"output_text","text":"{\"original_text\":\"source\",\"summary\":\"summary\",\"related_links\":[\"https://example.com/a\"]}"}
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
	if !candidate.SearchVerified || candidate.Result.OriginalText != "source" || candidate.Result.Model != "grok-4.6-20260901" {
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
            {"type":"message","status":"completed","content":[{"type":"output_text","text":"{\"original_text\":\"text\",\"summary\":\"summary\",\"related_links\":[],\"extra\":true}"}]}
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
