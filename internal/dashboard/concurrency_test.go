package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// TestTaxonomyCacheIsSafeForConcurrentReaders hammers the cache so a future
// change that mutates shared state would be caught by the race detector.
func TestTaxonomyCacheIsSafeForConcurrentReaders(t *testing.T) {
	backend := &fakeBackend{}
	cache := newTaxonomyCache(backend)

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 20 {
				catalog, err := cache.Catalog(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if catalog.Version != "test-v1" {
					t.Errorf("version = %q", catalog.Version)
					return
				}
			}
		}()
	}
	group.Wait()

	// Every reader shares one load, which is the point of the cache.
	if backend.taxonomyCalls != 1 {
		t.Fatalf("taxonomyCalls = %d, want 1", backend.taxonomyCalls)
	}
}

func TestTaxonomyCacheInvalidateDuringReadsIsSafe(t *testing.T) {
	backend := &fakeBackend{}
	cache := newTaxonomyCache(backend)
	stop := make(chan struct{})
	invalidatorDone := make(chan struct{})

	go func() {
		defer close(invalidatorDone)
		for {
			select {
			case <-stop:
				return
			default:
				cache.Invalidate()
			}
		}
	}()

	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 20 {
				if _, err := cache.Catalog(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	<-invalidatorDone
}

// TestBackstageSummaryCacheIsSafeForConcurrentPolls covers the mutex-protected
// aggregate that several browser tabs can request at once.
func TestBackstageSummaryCacheIsSafeForConcurrentPolls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{jobs: map[int64]*cairn.Job{}, claimErrs: map[int64]error{}}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)

	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 10 {
				if _, err := server.buildBackstageSummary(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	group.Wait()

	// The cache must collapse the polls into a bounded number of upstream calls
	// rather than one per request.
	if calls := backend.listCalls; calls > len(backstageAttentionStatuses)*2 {
		t.Fatalf("listCalls = %d, want the cache to collapse concurrent polls", calls)
	}
}

// TestQueueAccountingIsRaceFree exercises concurrent admission and completion,
// because the counter is written under a mutex and read atomically.
func TestQueueAccountingIsRaceFree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processing := make(chan int64, 64)
	backend := &fakeBackend{
		jobs:      map[int64]*cairn.Job{},
		claimErrs: map[int64]error{},
	}
	for id := int64(1); id <= 40; id++ {
		backend.jobs[id] = &cairn.Job{ID: id, URL: "https://x.com/a/status/1", Attempt: 1, LeaseToken: "t", LeaseUntil: "u"}
	}
	server := New(ctx, startedTracker(), backend, &fakeProcessor{processed: processing}, testLogger(), 4)

	var accepted atomic.Int64
	var group sync.WaitGroup
	for id := int64(1); id <= 40; id++ {
		group.Add(1)
		go func() {
			defer group.Done()
			body := `{"ids":[` + strconv.FormatInt(id, 10) + `]}`
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/bookmarks/process", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code == 202 {
				accepted.Add(1)
			}
		}()
	}
	group.Wait()

	// Let the workers drain what was admitted.
	deadline := time.Now().Add(5 * time.Second)
	for server.queued.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := server.queued.Load(); got != 0 {
		t.Fatalf("queued = %d after draining, want 0", got)
	}
	if accepted.Load() == 0 {
		t.Fatal("no request was accepted")
	}
}
