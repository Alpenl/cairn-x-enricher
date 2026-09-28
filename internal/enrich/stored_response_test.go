package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRetrieveStoredResponseIsReadOnlyAndRedactsPayload(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/responses/resp_123" ||
			request.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("lookup request = %s %s, auth %q", request.Method, request.URL.Path,
				request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"object":"response","id":"resp_123","status":"completed",` +
			`"model":"grok-test","created_at":1790620000,"input":"private prompt",` +
			`"output":[{"content":[{"text":"private model body"}]}],` +
			`"usage":{"input_tokens":100,"output_tokens":20,"cost_in_usd_ticks":1234}}`))
	}))
	defer server.Close()
	summary, err := RetrieveStoredResponse(context.Background(), server.URL+"/v1", "fixture-key",
		"resp_123", server.Client())
	if err != nil || summary.ID != "resp_123" || summary.Usage.CostUSDTicks == nil ||
		*summary.Usage.CostUSDTicks != 1234 || requests.Load() != 1 {
		t.Fatalf("stored response summary = %+v, requests=%d, err=%v", summary, requests.Load(), err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || strings.Contains(string(encoded), "private prompt") ||
		strings.Contains(string(encoded), "private model body") {
		t.Fatalf("lookup summary leaked content: %s, err=%v", encoded, err)
	}
	if _, err := RetrieveStoredResponse(context.Background(), server.URL+"/v1", "fixture-key",
		"bad/../id", server.Client()); err == nil || requests.Load() != 1 {
		t.Fatalf("invalid ID reached provider: requests=%d, err=%v", requests.Load(), err)
	}
}

func TestRetrieveStoredResponseNeverInfersBillingFromMissingOrInvalidData(t *testing.T) {
	for _, scenario := range []struct {
		name, body string
		status     int
		notFound   bool
	}{
		{name: "not found", status: http.StatusNotFound, notFound: true},
		{name: "redirect", status: http.StatusFound},
		{name: "wrong ID", status: http.StatusOK, body: `{"object":"response","id":"other","status":"completed","model":"grok-test"}`},
		{name: "malformed", status: http.StatusOK, body: `{"object":"response","id":`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodGet {
					t.Errorf("unexpected provider method %s", request.Method)
				}
				writer.Header().Set("Location", "/other")
				writer.WriteHeader(scenario.status)
				_, _ = writer.Write([]byte(scenario.body))
			}))
			defer server.Close()
			_, err := RetrieveStoredResponse(context.Background(), server.URL+"/v1", "fixture-key",
				"resp_123", server.Client())
			if err == nil || errors.Is(err, ErrStoredResponseNotFound) != scenario.notFound || requests.Load() != 1 {
				t.Fatalf("lookup failure = %v, requests=%d", err, requests.Load())
			}
		})
	}
}

func TestRetrieveStoredSourceRequiresBoundIdentityAndSearchEvidence(t *testing.T) {
	var output = `{"object":"response","id":"resp_123","status":"completed","model":"grok-test",` +
		`"output":[{"type":"x_search_call","status":"completed"},{"type":"message",` +
		`"content":[{"type":"output_text","text":"{\"original_text\":\"Private source\",` +
		`\"original_language\":\"en\",\"context_text\":\"\",\"related_links\":[],\"image_urls\":[]}"}]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected paid method %s", request.Method)
		}
		_, _ = writer.Write([]byte(output))
	}))
	defer server.Close()
	summary, source, err := RetrieveStoredSource(context.Background(), server.URL+"/v1", "fixture-key",
		"resp_123", server.Client())
	if err != nil || summary.ID != "resp_123" || source.OriginalText != "Private source" ||
		source.Model != "grok-test" {
		t.Fatalf("stored source = %+v, summary=%+v, err=%v", source, summary, err)
	}
	output = strings.Replace(output, `"type":"x_search_call","status":"completed"`,
		`"type":"x_search_call","status":"incomplete"`, 1)
	if _, _, err := RetrieveStoredSource(context.Background(), server.URL+"/v1", "fixture-key",
		"resp_123", server.Client()); err == nil {
		t.Fatal("source without completed search evidence was accepted")
	}
}
