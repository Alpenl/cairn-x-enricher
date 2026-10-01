package cairn

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

type requestTimingKey struct{}

// RequestTiming contains only numeric, request-local diagnostics. Durations
// from multiple upstream requests are cumulative, including parallel calls.
type RequestTiming struct {
	mu       sync.Mutex
	calls    int
	upstream time.Duration
	workerMS float64
	d1MS     float64
}

// WithRequestTiming attaches a request-local collector to the returned context.
func WithRequestTiming(ctx context.Context) (context.Context, *RequestTiming) {
	timing := &RequestTiming{}
	return context.WithValue(ctx, requestTimingKey{}, timing), timing
}

func (timing *RequestTiming) record(elapsed time.Duration, serverTiming string) {
	if timing == nil {
		return
	}
	timing.mu.Lock()
	defer timing.mu.Unlock()
	timing.calls++
	timing.upstream += elapsed
	// Never relay arbitrary upstream descriptions, identifiers, or headers.
	if len(serverTiming) > 4096 {
		return
	}
	for _, metric := range strings.Split(serverTiming, ",") {
		parts := strings.Split(metric, ";")
		name := strings.TrimSpace(parts[0])
		if name != "total" && name != "db" {
			continue
		}
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || key != "dur" {
				continue
			}
			milliseconds, err := strconv.ParseFloat(value, 64)
			if err == nil && !math.IsNaN(milliseconds) && !math.IsInf(milliseconds, 0) && milliseconds >= 0 && milliseconds <= 86400000 {
				if name == "total" {
					timing.workerMS += milliseconds
				} else {
					timing.d1MS += milliseconds
				}
			}
			break
		}
	}
}

// Header returns numeric Server-Timing metrics without upstream identifiers.
func (timing *RequestTiming) Header(elapsed time.Duration) string {
	timing.mu.Lock()
	defer timing.mu.Unlock()
	return fmt.Sprintf("nas;dur=%.2f, upstream;dur=%.2f, worker;dur=%.2f, d1;dur=%.2f, upstream_calls;desc=\"%d\"",
		float64(elapsed)/float64(time.Millisecond), float64(timing.upstream)/float64(time.Millisecond), timing.workerMS, timing.d1MS, timing.calls)
}

type timedResponseBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (body *timedResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.finish)
	return err
}
