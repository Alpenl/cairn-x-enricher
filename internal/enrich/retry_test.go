package enrich

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 502 can arrive after a provider has executed a paid request. Its receipt
// does not prove that a second POST is free, even with Idempotency-Key set.
func TestAmbiguousHTTPFailureDoesNotStartAnotherModelRequest(t *testing.T) {
	for _, operation := range []string{"fetch", "reading", "legacy"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls++
				writer.WriteHeader(http.StatusBadGateway)
				_, _ = writer.Write([]byte(`{"error":{"type":"upstream_error"}}`))
			}))
			defer server.Close()
			client := NewResponsesClient(server.URL, "key", "model", 1024, "", server.Client(), testTaxonomy())
			input := Input{ID: 7, URL: "https://x.com/a/status/7", Attempt: 1, SourceText: "saved source"}
			var err error
			switch operation {
			case "fetch":
				input.SourceText = ""
				_, err = client.FetchSource(context.Background(), input)
			case "reading":
				_, err = client.Transform(context.Background(), input)
			case "legacy":
				input.SourceText = ""
				_, err = client.Generate(context.Background(), input)
			}
			var modelErr *ModelHTTPError
			if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway || calls != 1 {
				t.Fatalf("ambiguous response: calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestReadModelHTTPErrorWithEmptyBody(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}
	err := readModelHTTPError(response)
	var modelErr *ModelHTTPError
	if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("readModelHTTPError() = %T %v", err, err)
	}
	if got := modelErr.Error(); got != "HTTP 502" {
		t.Fatalf("Error() = %q, want HTTP 502", got)
	}
}

func TestReadingCorrelationKeySurvivesJobRetry(t *testing.T) {
	first := modelIdempotencyKey(Input{ID: 7, Attempt: 1}, "reading-abc123", 1)
	retried := modelIdempotencyKey(Input{ID: 7, Attempt: 2}, "reading-abc123", 1)
	if first != retried {
		t.Fatalf("reading key changed across a job retry: %q vs %q", first, retried)
	}
	// The header is a correlation hint. It is not proof of provider-side
	// deduplication, so the network attempt above remains bounded to one.
	if modelIdempotencyKey(Input{ID: 7, Attempt: 1}, "post", 1) ==
		modelIdempotencyKey(Input{ID: 7, Attempt: 2}, "post", 1) {
		t.Fatal("different legacy attempts must have distinct correlation keys")
	}
}

func TestIsXSearchOutputRecognisesBothProtocolForms(t *testing.T) {
	cases := []struct {
		item responseOutputItem
		want bool
	}{
		{responseOutputItem{Type: "x_search_call"}, true},
		{responseOutputItem{Type: "custom_tool_call", Name: "x_thread_fetch"}, true},
		{responseOutputItem{Type: "custom_tool_call", Name: "x_keyword_search"}, true},
		{responseOutputItem{Type: "custom_tool_call", Name: "x_semantic_search"}, true},
		{responseOutputItem{Type: "custom_tool_call", Name: "x_user_search"}, true},
		{responseOutputItem{Type: "custom_tool_call", Name: "other_tool"}, false},
		{responseOutputItem{Type: "message"}, false},
		{responseOutputItem{Type: "reasoning"}, false},
	}
	for _, testCase := range cases {
		if got := isXSearchOutput(testCase.item); got != testCase.want {
			t.Errorf("isXSearchOutput(%+v) = %v, want %v", testCase.item, got, testCase.want)
		}
	}
}
