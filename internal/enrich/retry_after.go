package enrich

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxProviderRetryAfter is the largest provider cooldown we pass to the
// durable queue. A longer or malformed header cannot hold work indefinitely.
const MaxProviderRetryAfter = 10 * time.Minute

// ProviderRetryAfter reads TypeSafe's millisecond extension and the standard
// Retry-After seconds/date formats. The more precise millisecond header wins
// when both are present. Raw header text is never stored in a job error.
func ProviderRetryAfter(headers http.Header, now time.Time) time.Duration {
	if delay, ok := boundedRetryNumber(headers.Get("retry-after-ms"), float64(MaxProviderRetryAfter.Milliseconds()), time.Millisecond); ok {
		return delay
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if delay, ok := boundedRetryNumber(value, MaxProviderRetryAfter.Seconds(), time.Second); ok {
		return delay
	}
	if date, err := http.ParseTime(value); err == nil {
		return min(max(date.Sub(now), 0), MaxProviderRetryAfter)
	}
	return 0
}

func boundedRetryNumber(value string, ceiling float64, unit time.Duration) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n < 0 || n != n {
		return 0, false
	}
	if n >= ceiling {
		return MaxProviderRetryAfter, true
	}
	return time.Duration(n * float64(unit)), true
}
