package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderRecoverReadingUsesCurrentSourceAndExplicitCommit(t *testing.T) {
	operationKey := strings.Repeat("e", 64)
	var workerWrites, providerPosts atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/enrichment/provider-attempts/inspect":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer operator-secret" {
				t.Errorf("unexpected inspect request")
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"attempt": map[string]any{
				"operation_key": operationKey, "link_id": 17, "content_revision": 2,
				"current_content_revision": 2, "stage": "reading", "variant": "reading",
				"attempt_number": 1, "model": "grok-test", "state": "responded",
				"response_id": "resp_reading", "http_status": 200,
				"created_at": "2026-09-29T00:00:00Z"}})
		case "/api/enrichment/jobs/17/source":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer internal-secret" {
				t.Errorf("unexpected source request")
			}
			_, _ = writer.Write([]byte(`{"original_text":"private original source","original_language":"en",` +
				`"context_text":"","related_links":[],"image_urls":[],"model":"grok-test"}`))
		case "/api/enrichment/provider-attempts/recover-reading":
			workerWrites.Add(1)
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer operator-secret" {
				t.Errorf("unexpected recovery request")
			}
			var payload struct {
				OperationKey string `json:"operation_key"`
				ResponseID   string `json:"response_id"`
				Actor        string `json:"actor"`
				Reading      struct {
					Summary string `json:"summary"`
				} `json:"reading"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.OperationKey != operationKey || payload.ResponseID != "resp_reading" ||
				payload.Actor != "ops@example.org" || payload.Reading.Summary != "阅读摘要" {
				t.Error("recovery payload was not bound to the saved reading")
			}
			_, _ = writer.Write([]byte(`{"recovered":true,"id":17,"status":"completed",` +
				`"content_revision":2,"enriched_at":"2026-09-29T00:01:00Z"}`))
		default:
			t.Errorf("unexpected Worker path %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			providerPosts.Add(1)
		}
		if request.Method != http.MethodGet || request.URL.Path != "/v1/responses/resp_reading" ||
			request.Header.Get("Authorization") != "Bearer fixture-api-key" {
			t.Errorf("unexpected provider request %s %s", request.Method, request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"object":"response","id":"resp_reading","status":"completed",` +
			`"model":"grok-test","output":[{"type":"message","content":[{"type":"output_text",` +
			`"text":"{\"ai_title\":\"中文阅读辅助标题测试\",\"original_language\":\"en\",` +
			`\"translated_text\":\"完整译文\",\"summary\":\"阅读摘要\"}"}]}]}`))
	}))
	defer provider.Close()
	t.Setenv("CAIRN_API_BASE_URL", worker.URL)
	t.Setenv("CAIRN_ENRICHER_TOKEN", "internal-secret")
	t.Setenv("CAIRN_API_TOKEN", "app-secret")
	t.Setenv("CAIRN_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("GROK_MODELS_BASE_URL", provider.URL+"/v1")
	t.Setenv("XAI_API_KEY", "fixture-api-key")
	t.Setenv("GROK_MODEL", "grok-test")
	run := func(args ...string) string {
		t.Helper()
		command := newProviderRecoverReadingCommand()
		command.SetArgs(append([]string{"--operation-key", operationKey}, args...))
		var output bytes.Buffer
		command.SetOut(&output)
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "private original source") ||
			strings.Contains(output.String(), "完整译文") || strings.Contains(output.String(), operationKey) {
			t.Fatalf("operator output leaked private values: %s", output.String())
		}
		return output.String()
	}
	if output := run(); !strings.Contains(output, `"phase":"validated"`) || workerWrites.Load() != 0 {
		t.Fatalf("dry-run = %s, Worker writes=%d", output, workerWrites.Load())
	}
	if output := run("--commit", "--actor", "ops@example.org"); !strings.Contains(output, `"phase":"committed"`) || workerWrites.Load() != 1 || providerPosts.Load() != 0 {
		t.Fatalf("commit = %s, Worker writes=%d, provider POSTs=%d", output,
			workerWrites.Load(), providerPosts.Load())
	}
}
