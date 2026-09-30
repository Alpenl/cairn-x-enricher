package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderInspectDoesNotSendOrUnblockPaidWork(t *testing.T) {
	operationKey := "a"
	for len(operationKey) < 64 {
		operationKey += "a"
	}
	var workerReads, providerReads, providerStatus atomic.Int32
	storedResponseID := ""
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		workerReads.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/enrichment/provider-attempts/inspect" ||
			request.URL.Query().Get("operation_key") != operationKey ||
			request.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Errorf("Worker lookup = %s %s, auth %q", request.Method, request.URL,
				request.Header.Get("Authorization"))
		}
		var responseID any
		if storedResponseID != "" {
			responseID = storedResponseID
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"attempt": map[string]any{
			"operation_key": operationKey, "link_id": 17, "content_revision": 2,
			"stage": "fetch", "variant": "fetch_thread", "attempt_number": 1,
			"model": "grok-test", "state": "reserved", "response_id": responseID,
			"created_at": "2026-09-29T00:00:00Z"}})
	}))
	defer worker.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerReads.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/responses/resp_123" ||
			request.Header.Get("Authorization") != "Bearer fixture-api-key" {
			t.Errorf("provider lookup = %s %s, auth %q", request.Method, request.URL.Path,
				request.Header.Get("Authorization"))
		}
		if providerStatus.Load() == http.StatusNotFound {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(`{"object":"response","id":"resp_123","model":"grok-test",` +
			`"status":"completed","output":[{"content":[{"text":"private response"}]}]}`))
	}))
	defer provider.Close()
	t.Setenv("CAIRN_API_BASE_URL", worker.URL)
	t.Setenv("CAIRN_ENRICHER_TOKEN", "internal-secret")
	t.Setenv("CAIRN_API_TOKEN", "app-secret")
	t.Setenv("CAIRN_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("GROK_MODELS_BASE_URL", provider.URL+"/v1")
	t.Setenv("XAI_API_KEY", "fixture-api-key")
	t.Setenv("GROK_MODEL", "grok-test")
	run := func(args ...string) (map[string]any, error) {
		t.Helper()
		command := newProviderInspectCommand()
		command.SetArgs(append([]string{"--operation-key", operationKey}, args...))
		var output bytes.Buffer
		command.SetOut(&output)
		err := command.ExecuteContext(context.Background())
		if err != nil {
			return nil, err
		}
		var report map[string]any
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			return nil, fmt.Errorf("decode report: %w", err)
		}
		if bytes.Contains(output.Bytes(), []byte("private response")) ||
			bytes.Contains(output.Bytes(), []byte("fixture-api-key")) {
			t.Fatalf("inspection output leaked private data: %s", output.Bytes())
		}
		return report, nil
	}
	report, err := run("--response-id", "resp_123")
	if err != nil || report["provider_lookup"] != "found" || report["association"] != "unverified" ||
		report["billing"] != "not_determined_by_lookup" || workerReads.Load() != 1 || providerReads.Load() != 1 {
		t.Fatalf("unbound inspection = %#v, Worker=%d provider=%d, err=%v", report,
			workerReads.Load(), providerReads.Load(), err)
	}
	providerStatus.Store(http.StatusNotFound)
	report, err = run("--response-id", "resp_123")
	if err != nil || report["provider_lookup"] != "not_found_billing_unknown" ||
		report["billing"] != "not_determined_by_lookup" || providerReads.Load() != 2 {
		t.Fatalf("missing provider response = %#v, provider=%d, err=%v", report, providerReads.Load(), err)
	}
	report, err = run()
	if err != nil || report["provider_lookup"] != "not_attempted_no_response_id" || providerReads.Load() != 2 {
		t.Fatalf("no-ID inspection = %#v, provider=%d, err=%v", report, providerReads.Load(), err)
	}
	storedResponseID = "resp_original"
	if _, err := run("--response-id", "resp_123"); err == nil || providerReads.Load() != 2 {
		t.Fatalf("mismatched ledger ID reached provider: reads=%d, err=%v", providerReads.Load(), err)
	}
}
