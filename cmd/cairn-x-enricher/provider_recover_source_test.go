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

func TestProviderRecoverSourceUsesOnlyBoundGETAndExplicitCommit(t *testing.T) {
	operationKey := strings.Repeat("a", 64)
	var workerWrites, providerPosts atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Errorf("Worker authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/enrichment/provider-attempts/inspect":
			if request.Method != http.MethodGet || request.URL.Query().Get("operation_key") != operationKey {
				t.Errorf("unexpected inspect request %s %s", request.Method, request.URL)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"attempt": map[string]any{
				"operation_key": operationKey, "link_id": 17, "content_revision": 2,
				"stage": "fetch", "variant": "fetch_thread", "attempt_number": 1,
				"model": "grok-test", "state": "responded", "response_id": "resp_123",
				"http_status": 200, "created_at": "2026-09-29T00:00:00Z"}})
		case "/api/enrichment/provider-attempts/recover-source":
			workerWrites.Add(1)
			if request.Method != http.MethodPost {
				t.Errorf("unexpected recovery method %s", request.Method)
			}
			var payload struct {
				OperationKey string `json:"operation_key"`
				ResponseID   string `json:"response_id"`
				Actor        string `json:"actor"`
				Source       struct {
					OriginalText string `json:"original_text"`
				} `json:"source"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.OperationKey != operationKey || payload.ResponseID != "resp_123" ||
				payload.Actor != "ops@example.org" || payload.Source.OriginalText != "Private source" {
				t.Errorf("recovery payload fields did not match the bound response")
			}
			_, _ = writer.Write([]byte(`{"recovered":true,"id":17,"status":"source_saved","content_revision":3}`))
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
		if request.Method != http.MethodGet || request.URL.Path != "/v1/responses/resp_123" ||
			request.Header.Get("Authorization") != "Bearer fixture-api-key" {
			t.Errorf("unexpected provider request %s %s", request.Method, request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"object":"response","id":"resp_123","status":"completed",` +
			`"model":"grok-test","output":[{"type":"x_search_call","status":"completed"},` +
			`{"type":"message","content":[{"type":"output_text",` +
			`"text":"{\"original_text\":\"Private source\",\"original_language\":\"en\",` +
			`\"context_text\":\"\",\"related_links\":[],\"image_urls\":[]}"}]}]}`))
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
		command := newProviderRecoverSourceCommand()
		command.SetArgs(append([]string{"--operation-key", operationKey}, args...))
		var output bytes.Buffer
		command.SetOut(&output)
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "Private source") || strings.Contains(output.String(), operationKey) {
			t.Fatalf("operator output leaked private content or key: %s", output.String())
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
