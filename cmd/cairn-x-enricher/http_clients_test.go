package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUpstreamHTTPClientsHaveIndependentDeadlinesAndPools(t *testing.T) {
	worker := upstreamHTTPClient(20 * time.Second)
	provider := upstreamHTTPClient(3 * time.Minute)
	if worker.Timeout != 20*time.Second || provider.Timeout != 3*time.Minute || worker.Transport == provider.Transport {
		t.Fatalf("upstream clients share timeout or pool: worker=%s provider=%s", worker.Timeout, provider.Timeout)
	}
	if err := worker.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("authenticated request would follow redirect: %v", err)
	}
}

func TestWorkerTimeoutStopsHungUpstreamRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	client := upstreamHTTPClient(30 * time.Millisecond)
	started := time.Now()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("hung Worker request exceeded its own deadline: elapsed=%s error=%v", time.Since(started), err)
	}
}
