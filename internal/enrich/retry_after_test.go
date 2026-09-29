package enrich

import (
	"net/http"
	"testing"
	"time"
)

func TestProviderRetryAfterUsesBoundedHeaders(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name    string
		headers http.Header
		want    time.Duration
	}{
		{"seconds", http.Header{"Retry-After": {"90"}}, 90 * time.Second},
		{"http date", http.Header{"Retry-After": {now.Add(2 * time.Minute).Format(http.TimeFormat)}}, 2 * time.Minute},
		{"millisecond extension wins", http.Header{"Retry-After": {"90"}, "Retry-After-Ms": {"1250"}}, 1250 * time.Millisecond},
		{"large delay capped", http.Header{"Retry-After-Ms": {"86400000"}}, MaxProviderRetryAfter},
		{"bad ms falls back", http.Header{"Retry-After": {"45"}, "Retry-After-Ms": {"bad"}}, 45 * time.Second},
		{"negative ignored", http.Header{"Retry-After": {"-8"}}, 0},
		{"past date ignored", http.Header{"Retry-After": {now.Add(-time.Minute).Format(http.TimeFormat)}}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ProviderRetryAfter(testCase.headers, now); got != testCase.want {
				t.Fatalf("retry hint = %s, want %s", got, testCase.want)
			}
		})
	}
}
