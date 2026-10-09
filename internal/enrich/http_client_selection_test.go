package enrich

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type routeTransport func(*http.Request) (*http.Response, error)

func (f routeTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestURLOnlyNeverUsesEitherClientAndReadingUsesConfiguredClient(t *testing.T) {
	var fetchCalls, readingCalls int
	response := func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
	fetchClient := &http.Client{Transport: routeTransport(func(request *http.Request) (*http.Response, error) {
		fetchCalls++
		return response(request)
	})}
	readingClient := &http.Client{Transport: routeTransport(func(request *http.Request) (*http.Response, error) {
		readingCalls++
		return response(request)
	})}
	client := NewResponsesClient("https://fixture.invalid", "fixture", "grok", 1024, "", fetchClient, taxonomy.Catalog{})
	client.SetReadingHTTPClient(readingClient)
	_, _ = client.Generate(context.Background(), Input{URL: "https://x.com/a/status/1"})
	_, _ = client.Transform(context.Background(), Input{SourceText: "saved original"})
	_, _ = client.Transform(context.Background(), Input{SourceText: "canary", Canary: true})
	if fetchCalls != 0 || readingCalls != 2 {
		t.Fatalf("fetch/reading clients received %d/%d calls, want 0/2", fetchCalls, readingCalls)
	}
}
