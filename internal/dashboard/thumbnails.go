package dashboard

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // Register the bounded first-frame GIF decoder.
	"image/jpeg"
	_ "image/png" // Register PNG decoding for image.DecodeConfig and image.Decode.
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // Register the pure Go WebP decoder.
)

const thumbnailSize = 160
const thumbnailSourceBytes = 12 << 20
const thumbnailSourcePixels = 16_000_000
const thumbnailCacheBytes = 8 << 20
const thumbnailCacheItems = 128
const thumbnailCacheTTL = 10 * time.Minute

type conditionalImageBackend interface {
	GetImageConditional(context.Context, string, string) (*http.Response, error)
}

type thumbnail struct {
	key        string
	etag       string
	content    []byte
	sourceHash string
	cachedAt   time.Time
}

// Only derived bytes live in memory. A hit never bypasses an upstream owner,
// deletion and source-version check; neither original bytes nor disk are cached.
type thumbnailCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
	slots   chan struct{}
	flights map[string]*thumbnailFlight
}

type thumbnailFlight struct {
	done    chan struct{}
	content []byte
	err     error
}

// Each caller has already independently validated authorization and the source.
// Only CPU derivation is shared; a late deletion denial never joins this flight.
func (c *thumbnailCache) derive(ctx context.Context, sourceHash string, source []byte) ([]byte, error) {
	c.mu.Lock()
	if c.flights == nil {
		c.flights = make(map[string]*thumbnailFlight)
	}
	if flight := c.flights[sourceHash]; flight != nil {
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.content, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight := &thumbnailFlight{done: make(chan struct{})}
	c.flights[sourceHash] = flight
	c.mu.Unlock()
	flight.content, flight.err = deriveThumbnail(source)
	c.mu.Lock()
	delete(c.flights, sourceHash)
	close(flight.done)
	c.mu.Unlock()
	return flight.content, flight.err
}

func (c *thumbnailCache) get(key string) (thumbnail, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return thumbnail{}, false
	}
	value := entry.Value.(thumbnail)
	if time.Since(value.cachedAt) >= thumbnailCacheTTL {
		c.remove(entry)
		return thumbnail{}, false
	}
	c.order.MoveToFront(entry)
	return value, true
}

func (c *thumbnailCache) put(value thumbnail) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if old := c.entries[value.key]; old != nil {
		c.remove(old)
	}
	value.cachedAt = time.Now()
	c.entries[value.key] = c.order.PushFront(value)
	c.bytes += len(value.content)
	for c.bytes > thumbnailCacheBytes || len(c.entries) > thumbnailCacheItems {
		c.remove(c.order.Back())
	}
}

func (c *thumbnailCache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil {
		c.remove(entry)
	}
}

func (c *thumbnailCache) remove(entry *list.Element) {
	value := entry.Value.(thumbnail)
	delete(c.entries, value.key)
	c.bytes -= len(value.content)
	c.order.Remove(entry)
}

func (s *Server) getThumbnail(writer http.ResponseWriter, request *http.Request) {
	key := request.PathValue("key")
	cached, hit := s.thumbnails.get(key)
	var response *http.Response
	var err error
	if backend, ok := s.backend.(conditionalImageBackend); ok {
		validator := ""
		if hit {
			validator = cached.etag
		}
		response, err = backend.GetImageConditional(request.Context(), key, validator)
	} else {
		response, err = s.backend.GetImage(request.Context(), key)
	}
	if err != nil {
		s.thumbnails.forget(key)
		s.writeBackendError(writer, "get thumbnail", 0, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified {
		if !hit || cached.etag == "" || response.Header.Get("ETag") != cached.etag {
			writeError(writer, http.StatusBadGateway, "invalid_image_validator")
			return
		}
		writeThumbnail(writer, cached.content)
		return
	}
	// A changed or absent validator cannot leave an old image available for
	// another request if this cold download or decoder fails.
	if hit && response.Header.Get("ETag") != cached.etag {
		s.thumbnails.forget(key)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "image/") {
		writeError(writer, http.StatusBadGateway, "invalid_image")
		return
	}
	if response.ContentLength > thumbnailSourceBytes {
		s.thumbnails.forget(key)
		writeError(writer, http.StatusBadGateway, "invalid_image")
		return
	}
	// Bound original bytes, decoded pixels and concurrent decoders independently.
	// Waiting here is for cold derivation only, not an image-network semaphore.
	select {
	case s.thumbnails.slots <- struct{}{}:
		defer func() { <-s.thumbnails.slots }()
	case <-request.Context().Done():
		return
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, thumbnailSourceBytes+1))
	if err != nil || len(content) > thumbnailSourceBytes ||
		response.ContentLength > 0 && int64(len(content)) != response.ContentLength {
		writeError(writer, http.StatusBadGateway, "invalid_image")
		return
	}
	// A legacy 200 revalidates the full source. Avoid decoding unchanged bytes.
	hash := sha256.Sum256(content)
	contentKey := `"` + hex.EncodeToString(hash[:]) + `"`
	etag := response.Header.Get("ETag")
	if !strongThumbnailETag(etag) {
		etag = ""
	}
	// Another independently authorized request may already have derived this
	// source while this one waited for a decoder slot or downloaded a legacy 200.
	if current, exists := s.thumbnails.get(key); exists && current.sourceHash == contentKey {
		cached, hit = current, true
	}
	if hit && contentKey == cached.sourceHash {
		cached.etag = etag
		if cached.etag == "" {
			cached.etag = contentKey
		}
		s.thumbnails.put(cached)
		writeThumbnail(writer, cached.content)
		return
	}
	derived, err := s.thumbnails.derive(request.Context(), contentKey, content)
	if err != nil {
		s.thumbnails.forget(key)
		// AVIF has no decoder in the Go image package. Keep its existing browser
		// support through a bounded original response, without caching originals.
		if response.Header.Get("Content-Type") == "image/avif" && len(content) >= 12 &&
			string(content[4:8]) == "ftyp" && (string(content[8:12]) == "avif" || string(content[8:12]) == "avis") {
			writer.Header().Set("Content-Type", "image/avif")
			writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			writer.Header().Set("X-Cairn-Thumbnail", "original")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(content)
			return
		}
		writeError(writer, http.StatusUnsupportedMediaType, "thumbnail_unsupported")
		return
	}
	if etag == "" {
		// A content hash still identifies a fully validated legacy 200; it may
		// be sent upstream as a conditional hint, never accepted without a 304.
		etag = contentKey
	}
	s.thumbnails.put(thumbnail{key: key, etag: etag, sourceHash: contentKey, content: derived})
	writeThumbnail(writer, derived)
}

func strongThumbnailETag(etag string) bool {
	return len(etag) >= 2 && len(etag) <= 256 && etag[0] == '"' && etag[len(etag)-1] == '"' &&
		!strings.ContainsAny(etag[1:len(etag)-1], "\"\r\n")
}

func deriveThumbnail(content []byte) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > thumbnailSourcePixels/config.Height {
		return nil, errors.New("unsupported or oversized thumbnail source")
	}
	source, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	width, height := config.Width, config.Height
	if width > thumbnailSize || height > thumbnailSize {
		if width >= height {
			height = max(1, height*thumbnailSize/width)
			width = thumbnailSize
		} else {
			width = max(1, width*thumbnailSize/height)
			height = thumbnailSize
		}
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(target, target.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(target, target.Bounds(), source, source.Bounds(), draw.Over, nil)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, target, &jpeg.Options{Quality: 76}); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func writeThumbnail(writer http.ResponseWriter, content []byte) {
	writer.Header().Set("Content-Type", "image/jpeg")
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}
