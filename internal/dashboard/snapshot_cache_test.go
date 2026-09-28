package dashboard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotCacheCoalescesPollsWithoutHoldingTheLockAcrossIO(t *testing.T) {
	var cache snapshotCache[int]
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	load := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return 42, nil
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			value, stale, err := cache.read(context.Background(), context.Background(), time.Minute, load)
			if err != nil || stale || value != 42 {
				t.Errorf("read = %d, stale %t, %v", value, stale, err)
			}
		})
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh never started")
	}
	// Invalidation has to take the cache lock while the backend is blocked.
	invalidated := make(chan struct{})
	go func() { cache.invalidate(); close(invalidated) }()
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("invalidation blocked on backend I/O")
	}
	releaseOnce.Do(func() { close(release) })
	group.Wait()
	if calls.Load() < 1 || calls.Load() > 2 {
		t.Fatalf("backend calls = %d, want one per epoch at most", calls.Load())
	}
}

func TestSnapshotCacheInvalidationSeparatesFlightsAndOldResultCannotRefill(t *testing.T) {
	var cache snapshotCache[int]
	var calls atomic.Int32
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOld) })
	load := func(context.Context) (int, error) {
		call := int(calls.Add(1))
		if call == 1 {
			close(oldStarted)
			<-releaseOld
		}
		return call, nil
	}
	oldResult := make(chan int, 1)
	go func() {
		value, _, _ := cache.read(context.Background(), context.Background(), time.Minute, load)
		oldResult <- value
	}()
	<-oldStarted
	cache.invalidate()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newValue, stale, err := cache.read(ctx, context.Background(), time.Minute, load)
	if err != nil || stale || newValue != 2 {
		t.Fatalf("new epoch read = %d, stale %t, %v", newValue, stale, err)
	}
	releaseOnce.Do(func() { close(releaseOld) })
	if old := <-oldResult; old != 1 {
		t.Fatalf("old waiter = %d, want its original snapshot", old)
	}
	value, stale, err := cache.read(ctx, context.Background(), time.Minute, load)
	if err != nil || stale || value != 2 || calls.Load() != 2 {
		t.Fatalf("final cache = %d, stale %t, error %v, calls %d", value, stale, err, calls.Load())
	}
}

func TestSnapshotCacheServesLastGoodValueDuringRefreshFailure(t *testing.T) {
	var cache snapshotCache[int]
	_, _, err := cache.read(context.Background(), context.Background(), time.Minute, func(context.Context) (int, error) { return 7, nil })
	if err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	cache.cachedAt = time.Now().Add(-2 * time.Minute)
	cache.mu.Unlock()
	var failures atomic.Int32
	load := func(context.Context) (int, error) { failures.Add(1); return 0, errors.New("upstream down") }
	for range 2 {
		value, stale, err := cache.read(context.Background(), context.Background(), time.Minute, load)
		if err != nil || !stale || value != 7 {
			t.Fatalf("fallback = %d, stale %t, %v", value, stale, err)
		}
	}
	if failures.Load() != 1 {
		t.Fatalf("failure retry called upstream %d times", failures.Load())
	}
}
