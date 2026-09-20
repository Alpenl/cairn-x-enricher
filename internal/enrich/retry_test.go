package enrich

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterDelayParsesBothHeaderForms(t *testing.T) {
	cases := []struct {
		name   string
		header string
		wantOK bool
		check  func(time.Duration) bool
	}{
		{name: "empty", header: "", wantOK: false},
		{name: "garbage", header: "soon", wantOK: false},
		{name: "seconds", header: "7", wantOK: true, check: func(d time.Duration) bool { return d == 7*time.Second }},
		{name: "zero", header: "0", wantOK: true, check: func(d time.Duration) bool { return d == 0 }},
		{name: "negative is ignored", header: "-5", wantOK: false},
		{
			name: "http date in the future", header: time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat),
			wantOK: true, check: func(d time.Duration) bool { return d > 0 && d <= 31*time.Second },
		},
		{
			name: "http date in the past", header: time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat),
			wantOK: true, check: func(d time.Duration) bool { return d == 0 },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			delay, ok := retryAfterDelay(testCase.header)
			if ok != testCase.wantOK {
				t.Fatalf("retryAfterDelay(%q) ok = %v, want %v", testCase.header, ok, testCase.wantOK)
			}
			if ok && testCase.check != nil && !testCase.check(delay) {
				t.Fatalf("retryAfterDelay(%q) = %v", testCase.header, delay)
			}
		})
	}
}

func TestModelRetryDelayPrefersRetryAfterAndStaysBounded(t *testing.T) {
	response := &http.Response{Header: http.Header{}}
	response.Header.Set("Retry-After", "2")
	if got := modelRetryDelay(response, 1); got != 2*time.Second {
		t.Fatalf("modelRetryDelay() = %v, want the advertised 2s", got)
	}

	// An absurd Retry-After must be clamped so a single response cannot stall
	// the whole batch far beyond the attempt deadline.
	response.Header.Set("Retry-After", "3600")
	if got := modelRetryDelay(response, 3); got != maxModelRetryDelay {
		t.Fatalf("modelRetryDelay() = %v, want the %v clamp", got, maxModelRetryDelay)
	}
}

func TestModelRetryDelayJittersButStaysBoundedWithoutRetryAfter(t *testing.T) {
	response := &http.Response{Header: http.Header{}}
	base := min(2*modelRetryBaseDelay, maxModelRetryDelay)
	for range 200 {
		got := modelRetryDelay(response, 2)
		if got < base-base/4 || got > base+base/4 {
			t.Fatalf("modelRetryDelay() = %v, want within 25%% of %v", got, base)
		}
	}
}

func TestWaitForRetryHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForRetry(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRetry() error = %v, want context.Canceled", err)
	}
}

func TestWaitForRetryReturnsAfterTheDelay(t *testing.T) {
	if err := waitForRetry(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("waitForRetry() error = %v", err)
	}
}

func TestWaitForRetryWithNonPositiveDelayReportsCancellation(t *testing.T) {
	// A zero delay is a no-op only when the context is still live.
	if err := waitForRetry(context.Background(), 0); err != nil {
		t.Fatalf("waitForRetry(ctx, 0) error = %v, want nil for a live context", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForRetry(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRetry(cancelled, 0) error = %v, want context.Canceled", err)
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

func TestRetryableModelStatusCoversTransientFailuresOnly(t *testing.T) {
	for _, status := range []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	} {
		if !retryableModelStatus(status) {
			t.Errorf("retryableModelStatus(%d) = false", status)
		}
	}
	for _, status := range []int{
		http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
	} {
		if retryableModelStatus(status) {
			t.Errorf("retryableModelStatus(%d) = true", status)
		}
	}
}

// TestReadModelHTTPErrorWithEmptyBody covers a gateway returning 5xx without a
// JSON body, where the status is the only diagnostic available.
func TestReadModelHTTPErrorWithEmptyBody(t *testing.T) {
	server := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       http.NoBody,
	}
	err := readModelHTTPError(server)
	var modelErr *ModelHTTPError
	if !errors.As(err, &modelErr) || modelErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("readModelHTTPError() = %T %v", err, err)
	}
	// The status leads the message so it survives downstream truncation.
	if got := modelErr.Error(); got != "HTTP 502" {
		t.Fatalf("Error() = %q, want %q", got, "HTTP 502")
	}
}

// TestReadingIdempotencyKeySurvivesJobRetry pins B02-T08/R25: a reading call
// must key the provider request on the persisted source fingerprint, not the
// job attempt, so a retry after a lost completion response reuses the same
// server-side result instead of paying for the same reading twice.
func TestReadingIdempotencyKeySurvivesJobRetry(t *testing.T) {
	first := modelIdempotencyKey(Input{ID: 7, Attempt: 1}, "reading-abc123", 1)
	retried := modelIdempotencyKey(Input{ID: 7, Attempt: 2}, "reading-abc123", 1)
	if first != retried {
		t.Fatalf("reading idempotency key changed across a job retry: %q vs %q", first, retried)
	}
	httpRetry := modelIdempotencyKey(Input{ID: 7, Attempt: 1}, "reading-abc123", 2)
	if httpRetry == first {
		t.Fatal("an in-place HTTP retry must still send a distinct key")
	}
	// Ordinary enrichment stays per-attempt: its input may legitimately change.
	if modelIdempotencyKey(Input{ID: 7, Attempt: 1}, "post", 1) == modelIdempotencyKey(Input{ID: 7, Attempt: 2}, "post", 1) {
		t.Fatal("enrichment keys must remain attempt-scoped")
	}
}
