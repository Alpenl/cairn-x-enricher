package enrich

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
)

// A 502 can arrive after a provider has executed a paid request. Its receipt
// does not prove that a second POST is free.
func TestAmbiguousHTTPFailureDoesNotStartAnotherModelRequest(t *testing.T) {
	for _, operation := range []string{"reading", "legacy"} {
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
			case "reading":
				_, err = client.Transform(context.Background(), input)
			case "legacy":
				_, err = client.Generate(context.Background(), input)
			}
			var modelErr *ModelHTTPError
			if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway || calls != 1 {
				t.Fatalf("ambiguous response: calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestProviderPOSTDoesNotGetTransportRetryOnReusedConnection(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte("ready"))
			return
		}
		posts.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = connection.Close() // The provider may have run and charged before the response vanished.
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	warmRequest, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create warm request: %v", err)
	}
	warm, err := client.Do(warmRequest)
	if err != nil {
		t.Fatalf("warm connection: %v", err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	_ = warm.Body.Close()
	var reused atomic.Bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) },
	})
	model := NewResponsesClient(server.URL, "key", "model", 1024, "", client, testTaxonomy())
	_, err = model.Transform(ctx, Input{ID: 7, URL: "https://x.com/a/status/7", Attempt: 1, SourceText: "saved source"})
	if err == nil || !reused.Load() || posts.Load() != 1 {
		t.Fatalf("reused=%v posts=%d error=%v; want one ambiguous attempt", reused.Load(), posts.Load(), err)
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
