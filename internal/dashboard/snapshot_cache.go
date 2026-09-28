package dashboard

import (
	"context"
	"sync"
	"time"
)

// A snapshot flight belongs to one invalidation epoch. Callers arriving after
// an edit must not join an older read, even if that read is still in progress.
type snapshotFlight[T any] struct {
	epoch uint64
	done  chan struct{}
	value T
	stale bool
	err   error
}

type snapshotCache[T any] struct {
	mu         sync.Mutex
	value      *T
	cachedAt   time.Time
	retryAfter time.Time
	epoch      uint64
	flight     *snapshotFlight[T]
}

const snapshotRefreshTimeout = 20 * time.Second
const snapshotFailureRetry = time.Second

func (c *snapshotCache[T]) read(ctx, workCtx context.Context, ttl time.Duration, load func(context.Context) (T, error)) (T, bool, error) {
	c.mu.Lock()
	if c.value != nil && time.Since(c.cachedAt) < ttl {
		value := *c.value
		c.mu.Unlock()
		return value, false, nil
	}
	if c.value != nil && time.Now().Before(c.retryAfter) {
		value := *c.value
		c.mu.Unlock()
		return value, true, nil
	}
	flight := c.flight
	if flight == nil {
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
		return flight.value, flight.stale, flight.err
	}
}

func (c *snapshotCache[T]) refresh(workCtx context.Context, load func(context.Context) (T, error), flight *snapshotFlight[T]) {
	ctx, cancel := context.WithTimeout(workCtx, snapshotRefreshTimeout)
	defer cancel()
	value, err := load(ctx)

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
			c.retryAfter = time.Time{}
		} else if c.value != nil {
			c.retryAfter = time.Now().Add(snapshotFailureRetry)
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
	c.flight = nil
	c.mu.Unlock()
}
