package dashboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type conditionalThumbnailBackend struct {
	fakeBackend
	mu          sync.Mutex
	content     []byte
	etag        string
	denied      bool
	calls       int
	conditional int
}

func (b *conditionalThumbnailBackend) GetImageConditional(_ context.Context, _ string, etag string) (*http.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.denied {
		return nil, &cairn.APIError{StatusCode: 404, Code: "not_found"}
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/jpeg"}, "Etag": []string{b.etag}}, Body: io.NopCloser(bytes.NewReader(b.content)), ContentLength: int64(len(b.content))}
	if etag != "" && etag == b.etag {
		b.conditional++
		response.StatusCode = 304
		response.Body = io.NopCloser(bytes.NewReader(nil))
		response.ContentLength = 0
	}
	return response, nil
}

func thumbnailFixture(t *testing.T, width, height int, red bool) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			shade := uint8((x*19 + y*29) % 80)
			pixel := color.RGBA{shade, shade, 220, 255}
			if red {
				pixel = color.RGBA{220, shade, shade, 255}
			}
			picture.SetRGBA(x, y, pixel)
		}
	}
	var body bytes.Buffer
	if err := jpeg.Encode(&body, picture, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func thumbnailRequest(t *testing.T, server *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/images/enrichment/1/"+testImageKey+query, nil))
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("thumbnail changed browser privacy policy")
	}
	return response
}

func TestThumbnailRevalidatesVersionAndDeletionOnEveryCacheHit(t *testing.T) {
	backend := &conditionalThumbnailBackend{content: thumbnailFixture(t, 800, 600, true), etag: `"v1"`}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	first := thumbnailRequest(t, server, "?privacy=1&size=160")
	config, _, err := image.DecodeConfig(bytes.NewReader(first.Body.Bytes()))
	if first.Code != 200 || err != nil || config.Width != 160 || config.Height != 120 || first.Body.Len()*5 >= len(backend.content) {
		t.Fatalf("thumbnail size/bytes: %d %v %v", first.Code, config, err)
	}
	second := thumbnailRequest(t, server, "?size=160")
	if second.Code != 200 || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) || backend.calls != 2 || backend.conditional != 1 {
		t.Fatal("cache hit skipped source validation or lost derived bytes")
	}
	backend.content = thumbnailFixture(t, 800, 600, false)
	backend.etag = `"v2"`
	changed := thumbnailRequest(t, server, "?size=160")
	if changed.Code != 200 || bytes.Equal(first.Body.Bytes(), changed.Body.Bytes()) {
		t.Fatal("ETag change served old private image")
	}
	backend.denied = true
	deleted := thumbnailRequest(t, server, "?size=160")
	if deleted.Code != 404 {
		t.Fatalf("deleted image: %d", deleted.Code)
	}
	if _, hit := server.thumbnails.get("enrichment/1/" + testImageKey); hit {
		t.Fatal("deletion left derived bytes in cache")
	}
}

func TestThumbnailLegacy200RevalidatesContentEvenWithUnchangedETag(t *testing.T) {
	backend := &fakeBackend{imageBody: string(thumbnailFixture(t, 320, 240, true))}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	first := thumbnailRequest(t, server, "?size=160")
	backend.imageBody = string(thumbnailFixture(t, 320, 240, false))
	second := thumbnailRequest(t, server, "?size=160")
	if first.Code != 200 || second.Code != 200 || bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("legacy source bytes reused by an untrustworthy validator")
	}
}

func TestThumbnailRejectsInvalidSizeAndBoundedDecode(t *testing.T) {
	server := New(t.Context(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for _, query := range []string{"?size=0", "?size=999999", "?size=160&size=160"} {
		if response := thumbnailRequest(t, server, query); response.Code != 400 {
			t.Fatalf("invalid size %s: %d", query, response.Code)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	bomb := encoded.Bytes()
	binary.BigEndian.PutUint32(bomb[16:20], 20000)
	binary.BigEndian.PutUint32(bomb[20:24], 20000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	if _, err := deriveThumbnail(bomb); err == nil {
		t.Fatal("oversized decoded pixels accepted")
	}
	if _, err := deriveThumbnail([]byte("<svg>private</svg>")); err == nil {
		t.Fatal("unsupported active content accepted")
	}
}

func TestThumbnailCacheHasItemByteAndAgeBounds(t *testing.T) {
	var cache thumbnailCache
	for i := range thumbnailCacheItems + 2 {
		cache.put(thumbnail{key: fmt.Sprint(i), content: []byte{1}})
	}
	if len(cache.entries) != thumbnailCacheItems {
		t.Fatal("item cache bound lost")
	}
	if _, hit := cache.get("0"); hit {
		t.Fatal("LRU eviction lost")
	}
	cache.put(thumbnail{key: "large", content: make([]byte, thumbnailCacheBytes)})
	if cache.bytes > thumbnailCacheBytes {
		t.Fatal("byte cache bound lost")
	}
	value := cache.entries["large"].Value.(thumbnail)
	value.cachedAt = time.Now().Add(-thumbnailCacheTTL - time.Second)
	cache.entries["large"].Value = value
	if _, hit := cache.get("large"); hit {
		t.Fatal("age cache bound lost")
	}
}
