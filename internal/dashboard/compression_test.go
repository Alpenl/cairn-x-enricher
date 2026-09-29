package dashboard

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func decodedGzip(t *testing.T, content []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestStaticGzipVariantRevalidatesItsOwnETag(t *testing.T) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	asset := webAssets["app.css"]
	if len(asset.gzipContent) == 0 {
		t.Fatal("stylesheet has no precompressed variant")
	}
	handler := server.Handler()
	request := func(encoding, etag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app.css", nil)
		if encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	plain := request("", "")
	compressed := request("br, gzip;q=0.5", "")
	if plain.Code != http.StatusOK || compressed.Code != http.StatusOK ||
		plain.Header().Get("Content-Encoding") != "" || compressed.Header().Get("Content-Encoding") != "gzip" ||
		plain.Header().Get("ETag") == compressed.Header().Get("ETag") ||
		compressed.Header().Get("Vary") != "Accept-Encoding" ||
		compressed.Header().Get("Content-Length") != strconv.Itoa(compressed.Body.Len()) ||
		!bytes.Equal(decodedGzip(t, compressed.Body.Bytes()), plain.Body.Bytes()) {
		t.Fatalf("static variants: plain=%d/%d compressed=%d/%d headers=%v",
			plain.Code, plain.Body.Len(), compressed.Code, compressed.Body.Len(), compressed.Header())
	}
	if compressed.Body.Len() >= plain.Body.Len()/2 {
		t.Fatalf("stylesheet gzip did not reduce transfer enough: %d vs %d", compressed.Body.Len(), plain.Body.Len())
	}
	match := request("gzip", compressed.Header().Get("ETag"))
	if match.Code != http.StatusNotModified || match.Body.Len() != 0 ||
		match.Header().Get("ETag") != compressed.Header().Get("ETag") ||
		match.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip revalidation: status=%d headers=%v body=%q", match.Code, match.Header(), match.Body.String())
	}
	if crossVariant := request("", compressed.Header().Get("ETag")); crossVariant.Code != http.StatusOK {
		t.Fatalf("gzip ETag revalidated the plain variant: %d", crossVariant.Code)
	}
	if disabled := request("gzip;q=0", ""); disabled.Header().Get("Content-Encoding") != "" {
		t.Fatalf("q=0 negotiated gzip: %v", disabled.Header())
	}
}

func TestApplicationShellGzipKeepsSecurityHeaders(t *testing.T) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()
	for _, path := range []string{"/", "/backstage", "/bookmarks/1"} {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		request.Header.Set("Accept-Encoding", "gzip")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "gzip" ||
			response.Header().Get("Vary") != "Accept-Encoding" ||
			response.Header().Get("Content-Security-Policy") == "" ||
			response.Header().Get("Cache-Control") != "no-store" ||
			response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) ||
			!bytes.Equal(decodedGzip(t, response.Body.Bytes()), appShell) {
			t.Fatalf("GET %s shell gzip: status=%d headers=%v", path, response.Code, response.Header())
		}
	}
}

func TestLargeJSONGzipKeepsImageResponseUnchanged(t *testing.T) {
	content := strings.Repeat("中文与 English source. ", 300)
	backend := &fakeBackend{page: cairn.BookmarkPage{Items: []cairn.Bookmark{{
		ID: 1, URL: "https://x.com/example/status/1", Note: content, Status: "completed",
	}}}, imageBody: "jpeg-data", imageLength: len("jpeg-data")}
	server := New(context.Background(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	jsonResponse := get("/api/bookmarks")
	if jsonResponse.Code != http.StatusOK || jsonResponse.Header().Get("Content-Encoding") != "gzip" ||
		jsonResponse.Header().Get("Vary") != "Accept-Encoding" ||
		!bytes.Contains(decodedGzip(t, jsonResponse.Body.Bytes()), []byte(content)) {
		t.Fatalf("large JSON was not compressed correctly: status=%d headers=%v", jsonResponse.Code, jsonResponse.Header())
	}
	image := get("/api/images/enrichment/1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg")
	if image.Code != http.StatusOK || image.Header().Get("Content-Encoding") != "" ||
		image.Header().Get("Content-Length") != strconv.Itoa(len("jpeg-data")) || image.Body.String() != "jpeg-data" {
		t.Fatalf("image proxy changed under gzip negotiation: status=%d headers=%v body=%q", image.Code, image.Header(), image.Body.String())
	}
}

func TestJSONCompressionFallsBackWithoutWaitingWhenSlotsAreFull(t *testing.T) {
	for range maxConcurrentCompressedResponses {
		gzipSlots <- struct{}{}
	}
	defer func() {
		for range maxConcurrentCompressedResponses {
			<-gzipSlots
		}
	}()
	handler := compressAPIResponses(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(strings.Repeat(`{"value":"long"}`, 400)))
	}))
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/example", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "" ||
		response.Header().Get("Vary") != "Accept-Encoding" || response.Body.Len() < minCompressedResponseBytes {
		t.Fatalf("full compression slots blocked or changed the response: code=%d headers=%v length=%d",
			response.Code, response.Header(), response.Body.Len())
	}
}

func BenchmarkDashboardJSONTransfer(b *testing.B) {
	items := make([]cairn.Bookmark, 50)
	for index := range items {
		unique := sha256.Sum256([]byte(strconv.Itoa(index)))
		items[index] = cairn.Bookmark{
			ID: int64(index + 1), URL: "https://x.com/example/status/123456789",
			Note: "A saved note with per-item context " + hex.EncodeToString(unique[:]), Status: "completed",
			AITitle: "A synthetic title for the transfer benchmark",
			Summary: "The same collection and response shape are used for both encodings, with varied content " +
				hex.EncodeToString(unique[:]),
		}
	}
	server := New(context.Background(), startedTracker(),
		&fakeBackend{page: cairn.BookmarkPage{Items: items}}, &fakeProcessor{}, testLogger(), 1)
	b.Cleanup(func() { server.Drain(time.Second) })
	handler := server.Handler()
	for _, encoding := range []string{"plain", "gzip"} {
		b.Run(encoding, func(b *testing.B) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks", nil)
			if encoding == "gzip" {
				request.Header.Set("Accept-Encoding", "gzip")
			}
			b.ReportAllocs()
			var wireBytes int
			for range b.N {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				wireBytes = response.Body.Len()
			}
			b.ReportMetric(float64(wireBytes), "wire-B/op")
		})
	}
}

func BenchmarkDashboardStaticTransfer(b *testing.B) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	b.Cleanup(func() { server.Drain(time.Second) })
	handler := server.Handler()
	for _, encoding := range []string{"plain", "gzip"} {
		b.Run(encoding, func(b *testing.B) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app.css", nil)
			if encoding == "gzip" {
				request.Header.Set("Accept-Encoding", "gzip")
			}
			b.ReportAllocs()
			var wireBytes int
			for range b.N {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				wireBytes = response.Body.Len()
			}
			b.ReportMetric(float64(wireBytes), "wire-B/op")
		})
	}
}
