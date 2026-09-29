package main

import (
	"net/http"
	"time"
)

// upstreamHTTPClient gives each upstream its own deadline and connection pool.
// Redirects are never followed across authenticated Worker/provider calls.
func upstreamHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 20
	transport.MaxIdleConnsPerHost = 10
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
