package enrich

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func canaryEnvelope(output string) string {
	encoded, _ := json.Marshal(output)
	return `{"status":"completed","model":"grok-test","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":` + string(encoded) + `}]}]}`
}

func TestCanaryAcceptsAHealthyContract(t *testing.T) {
	var sawSourceOnly bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := body["tools"]; exists {
			t.Errorf("canary request used tools: %#v", body["tools"])
		}
		sawSourceOnly = true
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(canaryEnvelope(`{"ai_title":"契约自检通过的中文标题","original_language":"en","original_text":"canary","translated_text":"自检译文","summary":"自检摘要","related_links":[],"image_urls":[],"classification":{"topics":[],"form":"","use":"","why_suggestion":"","entities":[],"uncertainty":true}}`)))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client(), testTaxonomy())
	if err := client.Canary(context.Background()); err != nil {
		t.Fatalf("Canary() error = %v", err)
	}
	if !sawSourceOnly {
		t.Fatal("canary did not send a request")
	}
}

func TestCanaryFailsWhenTheSchemaContractBreaks(t *testing.T) {
	cases := map[string]string{
		"unknown field":     `{"ai_title":"契约自检通过的中文标题","original_language":"en","original_text":"canary","translated_text":"自检译文","summary":"自检摘要","related_links":[],"image_urls":[],"classification":{"topics":[],"form":"","use":"","why_suggestion":"","entities":[],"uncertainty":true},"unexpected":1}`,
		"empty required":    `{"ai_title":"","original_language":"en","original_text":"canary","translated_text":"自检译文","summary":"自检摘要","related_links":[],"image_urls":[],"classification":{"topics":[],"form":"","use":"","why_suggestion":"","entities":[],"uncertainty":true}}`,
		"non-Chinese title": `{"ai_title":"A contract check title","original_language":"en","original_text":"canary","translated_text":"自检译文","summary":"自检摘要","related_links":[],"image_urls":[],"classification":{"topics":[],"form":"","use":"","why_suggestion":"","entities":[],"uncertainty":true}}`,
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(canaryEnvelope(output)))
			}))
			defer server.Close()

			client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client(), testTaxonomy())
			if err := client.Canary(context.Background()); err == nil {
				t.Fatal("Canary() error = nil, want a contract failure")
			}
		})
	}
}

func TestCanaryRejectsNonCompletedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"failed","model":"grok-test","output":[]}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client(), testTaxonomy())
	if err := client.Canary(context.Background()); err == nil {
		t.Fatal("Canary() error = nil, want a status failure")
	}
}

func TestCanaryDoesNotLeakTheAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key secret-value"}}`))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "secret-key-value", "model", 1024, "", server.Client(), testTaxonomy())
	err := client.Canary(context.Background())
	if err == nil {
		t.Fatal("Canary() error = nil, want a failure")
	}
	if strings.Contains(err.Error(), "secret-key-value") {
		t.Fatalf("canary error leaked the API key: %v", err)
	}
}

func TestJitterDurationStaysWithinBounds(t *testing.T) {
	base := 2 * time.Second
	for range 500 {
		got := jitterDuration(base, retryJitterPercent)
		if got < base-base/4 || got > base+base/4 {
			t.Fatalf("jitterDuration() = %v, want within 25%% of %v", got, base)
		}
	}
	if got := jitterDuration(0, 25); got != 0 {
		t.Fatalf("jitterDuration(0) = %v", got)
	}
	if got := jitterDuration(base, 0); got != base {
		t.Fatalf("jitterDuration(base, 0) = %v, want %v", got, base)
	}
}

func TestJitterDurationIsNotConstant(t *testing.T) {
	// A constant delay would recreate the synchronized retries jitter exists
	// to prevent.
	base := time.Second
	seen := map[time.Duration]struct{}{}
	for range 100 {
		seen[jitterDuration(base, retryJitterPercent)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("jitterDuration produced %d distinct values", len(seen))
	}
}

func TestShouldRetryModelRequestTreatsSlowFailuresAsTerminal(t *testing.T) {
	if shouldRetryModelRequest(http.StatusBadGateway, 1, slowModelFailure) {
		t.Fatal("slow model failure should not retry the same prompt")
	}
	if shouldRetryModelRequest(http.StatusBadGateway, 1, slowModelFailure+time.Second) {
		t.Fatal("slow model failure should not retry the same prompt")
	}
	if !shouldRetryModelRequest(http.StatusBadGateway, 1, time.Second) {
		t.Fatal("fast transient model failure should retry")
	}
	if shouldRetryModelRequest(http.StatusBadGateway, maxModelHTTPAttempts, time.Second) {
		t.Fatal("attempt budget was exceeded")
	}
	if shouldRetryModelRequest(http.StatusBadRequest, 1, time.Second) {
		t.Fatal("non-retryable status was retried")
	}
}

func TestDecodeStrictJSONRejectsTrailingData(t *testing.T) {
	var target map[string]any
	if err := decodeStrictJSON(strings.NewReader(`{"a":1}{"b":2}`), &target); err == nil {
		t.Fatal("decodeStrictJSON() accepted trailing data")
	}
}

func TestStructuredOutputDecodeIsBounded(t *testing.T) {
	// A model that ignores max_output_tokens must not be able to make the
	// process allocate unbounded memory while decoding the payload.
	oversized := `{"ai_title":"` + strings.Repeat("字", maxModelOutputBytes) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, canaryEnvelope(oversized))
	}))
	defer server.Close()

	client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client(), testTaxonomy())
	if _, err := client.Generate(context.Background(), Input{
		ID: 1, URL: "https://x.com/a/status/1", Attempt: 1, SourceText: "trusted",
	}); err == nil {
		t.Fatal("Generate() accepted an unbounded structured payload")
	}
}
