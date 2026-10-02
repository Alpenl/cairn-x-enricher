package cairn

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"
)

type requestTimingKey struct{}

// RequestTiming contains only numeric, request-local diagnostics. Durations
// from multiple upstream requests are cumulative, including parallel calls.
type RequestTiming struct {
	mu              sync.Mutex
	calls           int
	upstream        time.Duration
	workerMS        float64
	d1MS            float64
	r2MS            float64
	sqlCount        float64
	rowsRead        float64
	dbRoundTrips    float64
	rowsReadUnknown float64
	r2Calls         float64
	connect         time.Duration
	ttfb            time.Duration
	cacheWait       time.Duration
	cacheLoad       time.Duration
	cacheState      string
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
		count := name == "sql-count" || name == "rows-read" || name == "rows-read-unknown" || name == "db-round-trips" || name == "r2-calls"
		if name != "total" && name != "db" && name != "r2" && !count {
			continue
		}
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || (!count && key != "dur") || (count && key != "desc") {
				continue
			}
			if count {
				value = strings.Trim(value, "\"")
			}
			milliseconds, err := strconv.ParseFloat(value, 64)
			limit := float64(86400000)
			if count {
				limit = 1e12
			}
			if err == nil && !math.IsNaN(milliseconds) && !math.IsInf(milliseconds, 0) && milliseconds >= 0 && milliseconds <= limit && (!count || math.Trunc(milliseconds) == milliseconds) {
				switch name {
				case "total":
					timing.workerMS += milliseconds
				case "db":
					timing.d1MS += milliseconds
				case "r2":
					timing.r2MS += milliseconds
				case "sql-count":
					timing.sqlCount += milliseconds
				case "rows-read":
					timing.rowsRead += milliseconds
				case "rows-read-unknown":
					timing.rowsReadUnknown += milliseconds
				case "db-round-trips":
					timing.dbRoundTrips += milliseconds
				case "r2-calls":
					timing.r2Calls += milliseconds
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
	header := fmt.Sprintf("nas;dur=%.2f, upstream;dur=%.2f, worker;dur=%.2f, d1;dur=%.2f, upstream_calls;desc=\"%d\", upstream_connect;dur=%.2f, upstream_ttfb;dur=%.2f, r2;dur=%.2f, sql-count;desc=\"%.0f\", rows-read;desc=\"%.0f\", rows-read-unknown;desc=\"%.0f\", db-round-trips;desc=\"%.0f\", r2-calls;desc=\"%.0f\"",
		float64(elapsed)/float64(time.Millisecond), float64(timing.upstream)/float64(time.Millisecond), timing.workerMS, timing.d1MS, timing.calls,
		float64(timing.connect)/float64(time.Millisecond), float64(timing.ttfb)/float64(time.Millisecond), timing.r2MS, timing.sqlCount, timing.rowsRead, timing.rowsReadUnknown, timing.dbRoundTrips, timing.r2Calls)
	if timing.cacheState != "" {
		header += fmt.Sprintf(", cache;desc=\"%s\", cache_wait;dur=%.2f, cache_load;dur=%.2f", timing.cacheState,
			float64(timing.cacheWait)/float64(time.Millisecond), float64(timing.cacheLoad)/float64(time.Millisecond))
	}
	return header
}

// RecordCacheTiming attributes a shared refresh to each waiting request without
// binding its lifetime to the first browser. Upstream durations are cumulative,
// whereas cache_wait and cache_load measure wall time and may overlap.
func RecordCacheTiming(ctx context.Context, state string, wait, load time.Duration, shared *RequestTiming) {
	timing, _ := ctx.Value(requestTimingKey{}).(*RequestTiming)
	if timing == nil || (state != "hit" && state != "stale" && state != "load" && state != "shared") {
		return
	}
	if shared != nil && shared != timing {
		shared.mu.Lock()
		timing.mu.Lock()
		timing.calls += shared.calls
		timing.upstream += shared.upstream
		timing.workerMS += shared.workerMS
		timing.d1MS += shared.d1MS
		timing.r2MS += shared.r2MS
		timing.sqlCount += shared.sqlCount
		timing.rowsRead += shared.rowsRead
		timing.rowsReadUnknown += shared.rowsReadUnknown
		timing.dbRoundTrips += shared.dbRoundTrips
		timing.r2Calls += shared.r2Calls
		timing.connect += shared.connect
		timing.ttfb += shared.ttfb
		timing.mu.Unlock()
		shared.mu.Unlock()
	}
	timing.mu.Lock()
	timing.cacheWait += max(wait, 0)
	timing.cacheLoad += max(load, 0)
	timing.cacheState = state
	timing.mu.Unlock()
}

func traceUpstream(ctx context.Context) context.Context {
	timing, _ := ctx.Value(requestTimingKey{}).(*RequestTiming)
	if timing == nil {
		return ctx
	}
	var mu sync.Mutex
	var connecting, wrote time.Time
	trace := &httptrace.ClientTrace{
		GetConn: func(string) { mu.Lock(); connecting = time.Now(); mu.Unlock() },
		GotConn: func(httptrace.GotConnInfo) {
			mu.Lock()
			elapsed := time.Since(connecting)
			mu.Unlock()
			timing.mu.Lock()
			timing.connect += elapsed
			timing.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { mu.Lock(); wrote = time.Now(); mu.Unlock() },
		GotFirstResponseByte: func() {
			mu.Lock()
			started := wrote
			mu.Unlock()
			if !started.IsZero() {
				timing.mu.Lock()
				timing.ttfb += time.Since(started)
				timing.mu.Unlock()
			}
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}

type timedResponseBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (timing *RequestTiming) recordTransfer(elapsed time.Duration) {
	timing.mu.Lock()
	timing.upstream += elapsed
	timing.mu.Unlock()
}

func (body *timedResponseBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err != nil {
		body.once.Do(body.finish)
	}
	return count, err
}

func (body *timedResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.finish)
	return err
}
