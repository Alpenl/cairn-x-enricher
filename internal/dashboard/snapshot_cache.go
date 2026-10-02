package dashboard

import (
	"context"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// A snapshot flight belongs to one invalidation epoch. Callers arriving after
// an edit must not join an older read, even if that read is still in progress.
type snapshotFlight[T any] struct {
	epoch  uint64
	done   chan struct{}
	value  T
	stale  bool
	err    error
	timing *cairn.RequestTiming
	load   time.Duration
}

type snapshotCache[T any] struct {
	mu         sync.Mutex
	value      *T
	cachedAt   time.Time
	retryAfter time.Time
	epoch      uint64
	flight     *snapshotFlight[T]
	now        func() time.Time
	lastErr    error
}

const snapshotRefreshTimeout = 20 * time.Second
const snapshotFailureRetry = time.Second

func (c *snapshotCache[T]) read(ctx, workCtx context.Context, ttl time.Duration, load func(context.Context) (T, error)) (T, bool, error) {
	started := time.Now()
	c.mu.Lock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.value != nil && now.Sub(c.cachedAt) < ttl {
		value := *c.value
		c.mu.Unlock()
		cairn.RecordCacheTiming(ctx, "hit", 0, 0, nil)
		return value, false, nil
	}
	if now.Before(c.retryAfter) {
		var value T
		stale := c.value != nil
		if stale {
			value = *c.value
		}
		err := c.lastErr
		c.mu.Unlock()
		cairn.RecordCacheTiming(ctx, "stale", 0, 0, nil)
		if stale {
			err = nil
		}
		return value, stale, err
	}
	flight := c.flight
	state := "shared"
	if flight == nil {
		state = "load"
		flight = &snapshotFlight[T]{epoch: c.epoch, done: make(chan struct{})}
		c.flight = flight
		// The refresh has a server lifetime and its own deadline. One browser
		// disconnect must not cancel a read shared by other waiting tabs.
		go c.refresh(workCtx, load, flight)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		var zero T
		return zero, false, ctx.Err()
	case <-flight.done:
		cairn.RecordCacheTiming(ctx, state, time.Since(started), flight.load, flight.timing)
		return flight.value, flight.stale, flight.err
	}
}

func (c *snapshotCache[T]) refresh(workCtx context.Context, load func(context.Context) (T, error), flight *snapshotFlight[T]) {
	ctx, cancel := context.WithTimeout(workCtx, snapshotRefreshTimeout)
	defer cancel()
	ctx, flight.timing = cairn.WithRequestTiming(ctx)
	started := time.Now()
	value, err := load(ctx)
	flight.load = time.Since(started)

	c.mu.Lock()
	if err != nil && c.value != nil {
		flight.value = *c.value
		flight.stale = true
	} else {
		flight.value = value
		flight.err = err
	}
	if c.epoch == flight.epoch && c.flight == flight {
		c.flight = nil
		if err == nil {
			c.value = &value
			c.cachedAt = time.Now()
			if c.now != nil {
				c.cachedAt = c.now()
			}
			c.retryAfter = time.Time{}
			c.lastErr = nil
		} else {
			now := time.Now()
			if c.now != nil {
				now = c.now()
			}
			c.retryAfter = now.Add(snapshotFailureRetry)
			c.lastErr = err
		}
	}
	close(flight.done)
	c.mu.Unlock()
}

func (c *snapshotCache[T]) invalidate() {
	c.mu.Lock()
	c.epoch++
	c.cachedAt = time.Time{}
	c.retryAfter = time.Time{}
	c.lastErr = nil
	c.flight = nil
	c.mu.Unlock()
}
