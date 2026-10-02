package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type thumbnailClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *thumbnailClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *thumbnailClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(duration)
	c.mu.Unlock()
}

func TestThumbnailValidatedHitsRenewIdleLifetime(t *testing.T) {
	clock := &thumbnailClock{at: time.Unix(1_000, 0)}
	backend := &conditionalThumbnailBackend{content: thumbnailFixture(t, 320, 240, true), etag: `"v1"`}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	server.thumbnails.now = clock.now
	defer server.Drain(time.Second)
	first := thumbnailRequest(t, server, "?size=160")
	if first.Code != http.StatusOK {
		t.Fatalf("cold image: %d", first.Code)
	}
	for _, interval := range []time.Duration{9 * time.Minute, 2 * time.Minute, 9 * time.Minute} {
		clock.advance(interval)
		response := thumbnailRequest(t, server, "?size=160")
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), first.Body.Bytes()) {
			t.Fatal("authorized active image expired or changed")
		}
	}
	if backend.calls != 4 || backend.conditional != 3 {
		t.Fatalf("active hits downloaded originals: calls=%d conditional=%d", backend.calls, backend.conditional)
	}
	// The cache survives twenty minutes of successful revalidation, but expires
	// at the idle boundary and fetches the source without the old validator.
	clock.advance(thumbnailCacheTTL)
	response := thumbnailRequest(t, server, "?size=160")
	if response.Code != http.StatusOK || backend.calls != 5 || backend.conditional != 3 {
		t.Fatalf("idle expiry failed: status=%d calls=%d conditional=%d", response.Code, backend.calls, backend.conditional)
	}
}

type delayedThumbnailBackend struct {
	conditionalThumbnailBackend
	blockMu sync.Mutex
	block   bool
	started chan struct{}
	resume  chan struct{}
}

func (b *delayedThumbnailBackend) GetImageConditional(ctx context.Context, key, etag string) (*http.Response, error) {
	response, err := b.conditionalThumbnailBackend.GetImageConditional(ctx, key, etag)
	b.blockMu.Lock()
	block := err == nil && response.StatusCode == http.StatusNotModified && b.block
	if block {
		b.block = false
	}
	b.blockMu.Unlock()
	if block {
		close(b.started)
		select {
		case <-b.resume:
		case <-ctx.Done():
			_ = response.Body.Close()
			return nil, ctx.Err()
		}
	}
	return response, err
}

func TestThumbnailLateValidationCannotRenewReplacementOrDeletion(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "deleted"}[deleted], func(t *testing.T) {
			clock := &thumbnailClock{at: time.Unix(1_000, 0)}
			backend := &delayedThumbnailBackend{
				conditionalThumbnailBackend: conditionalThumbnailBackend{content: thumbnailFixture(t, 320, 240, true), etag: `"v1"`},
				block:                       true, started: make(chan struct{}), resume: make(chan struct{}),
			}
			server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
			server.thumbnails.now = clock.now
			defer server.Drain(time.Second)
			if response := thumbnailRequest(t, server, "?size=160"); response.Code != http.StatusOK {
				t.Fatalf("cold image: %d", response.Code)
			}
			clock.advance(9 * time.Minute)
			done := make(chan struct{})
			go func() {
				defer close(done)
				thumbnailRequest(t, server, "?size=160")
			}()
			select {
			case <-backend.started:
			case <-time.After(time.Second):
				t.Fatal("old conditional request did not start")
			}
			backend.mu.Lock()
			backend.denied = deleted
			backend.etag = `"v2"`
			backend.content = thumbnailFixture(t, 320, 240, false)
			backend.mu.Unlock()
			updated := thumbnailRequest(t, server, "?size=160")
			if deleted && updated.Code != http.StatusNotFound || !deleted && updated.Code != http.StatusOK {
				t.Fatalf("updated image: %d", updated.Code)
			}
			clock.advance(time.Minute)
			close(backend.resume)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("old validation did not finish")
			}
			key := "enrichment/1/" + testImageKey
			if deleted {
				if _, hit := server.thumbnails.get(key); hit {
					t.Fatal("old 304 resurrected deleted image")
				}
				return
			}
			clock.advance(9 * time.Minute)
			if _, hit := server.thumbnails.get(key); hit {
				t.Fatal("old 304 extended the replacement's idle expiry")
			}
		})
	}
}

func TestThumbnailRenewDoesNotResurrectExpiredEvictedOrReplacedEntry(t *testing.T) {
	for _, action := range []string{"expired", "evicted", "replaced"} {
		t.Run(action, func(t *testing.T) {
			clock := &thumbnailClock{at: time.Unix(1_000, 0)}
			cache := thumbnailCache{now: clock.now}
			cache.put(thumbnail{key: "image", etag: `"v1"`, sourceHash: "bytes", content: []byte{1}})
			original, hit := cache.get("image")
			if !hit {
				t.Fatal("cache fixture missing")
			}
			switch action {
			case "expired":
				clock.advance(thumbnailCacheTTL)
			case "evicted":
				cache.forget("image")
			case "replaced":
				// Even replacement with identical bytes is a new entry: an older
				// request cannot renew it after eviction or invalidation.
				cache.put(original)
			}
			if cache.renew(original) {
				t.Fatal("stale validation renewed cache entry")
			}
		})
	}
}

type canceledThumbnailBackend struct {
	conditionalThumbnailBackend
	cancel  context.CancelFunc
	invalid bool
}

func (b *canceledThumbnailBackend) GetImageConditional(ctx context.Context, key, etag string) (*http.Response, error) {
	response, err := b.conditionalThumbnailBackend.GetImageConditional(ctx, key, etag)
	if err == nil && response.StatusCode == http.StatusNotModified {
		if b.invalid {
			response.Header.Set("ETag", `"wrong-version"`)
		} else {
			b.cancel()
		}
	}
	return response, err
}

func TestThumbnailCanceledOrInvalidValidationDoesNotRenew(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "invalid_etag"}[invalid], func(t *testing.T) {
			clock := &thumbnailClock{at: time.Unix(1_000, 0)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend := &canceledThumbnailBackend{
				conditionalThumbnailBackend: conditionalThumbnailBackend{content: thumbnailFixture(t, 320, 240, true), etag: `"v1"`},
				cancel:                      cancel, invalid: invalid,
			}
			server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
			server.thumbnails.now = clock.now
			defer server.Drain(time.Second)
			thumbnailRequest(t, server, "?size=160")
			key := "enrichment/1/" + testImageKey
			clock.advance(9 * time.Minute)
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/images/"+key+"?size=160", nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if invalid && response.Code != http.StatusBadGateway {
				t.Fatalf("invalid validator accepted: %d", response.Code)
			}
			clock.advance(time.Minute)
			if _, hit := server.thumbnails.get(key); hit {
				t.Fatal("canceled or invalid validation extended idle expiry")
			}
		})
	}
}
