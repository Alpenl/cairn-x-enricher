package dashboard

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
)

const minCompressedResponseBytes = 4096
const minCompressedAssetBytes = 1024

// Active compressors retain working memory. Under a burst, serve a plain
// response once this fixed concurrency budget is used.
const maxConcurrentCompressedResponses = 8

var gzipSlots = make(chan struct{}, maxConcurrentCompressedResponses)

var gzipWriters = sync.Pool{New: func() any {
	// Reuse avoids allocating compressor state on every JSON response.
	writer, err := gzip.NewWriterLevel(io.Discard, gzip.StatelessCompression)
	if err != nil {
		panic(err)
	}
	return writer
}}

// Static files are compressed once when embedded assets are indexed. API
// responses use a small buffer so short JSON responses are sent unchanged.
func compressAPIResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api/") ||
			strings.HasPrefix(request.URL.Path, "/api/images/") {
			next.ServeHTTP(writer, request)
			return
		}
		appendVary(writer.Header(), "Accept-Encoding")
		if request.Method == http.MethodHead || !acceptsGzip(request) {
			next.ServeHTTP(writer, request)
			return
		}
		compressed := &gzipResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		defer compressed.finish()
		next.ServeHTTP(compressed, request)
	})
}

func acceptsGzip(request *http.Request) bool {
	for _, header := range request.Header.Values("Accept-Encoding") {
		for _, value := range strings.Split(header, ",") {
			parts := strings.Split(value, ";")
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
				continue
			}
			quality := 1.0
			for _, parameter := range parts[1:] {
				name, raw, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
			if quality > 0 {
				return true
			}
		}
	}
	return false
}

func appendVary(header http.Header, field string) {
	for _, value := range header.Values("Vary") {
		for _, existing := range strings.Split(value, ",") {
			if strings.TrimSpace(existing) == "*" || strings.EqualFold(strings.TrimSpace(existing), field) {
				return
			}
		}
	}
	header.Add("Vary", field)
}

type gzipResponseWriter struct {
	http.ResponseWriter
	status     int
	statusSet  bool
	buffer     []byte
	started    bool
	compressed *gzip.Writer
}

func (writer *gzipResponseWriter) WriteHeader(status int) {
	if writer.started || writer.statusSet {
		return
	}
	writer.status = status
	writer.statusSet = true
}

func (writer *gzipResponseWriter) Write(content []byte) (int, error) {
	if writer.started {
		if writer.compressed != nil {
			return writer.compressed.Write(content)
		}
		return writer.ResponseWriter.Write(content)
	}
	writer.buffer = append(writer.buffer, content...)
	if len(writer.buffer) >= minCompressedResponseBytes {
		if err := writer.start(); err != nil {
			return 0, err
		}
	}
	return len(content), nil
}

func (writer *gzipResponseWriter) start() error {
	if writer.started {
		return nil
	}
	writer.started = true
	header := writer.Header()
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(header.Get("Content-Type"), ";", 2)[0]))
	compressible := writer.status >= 200 && writer.status < 300 && writer.status != http.StatusNoContent &&
		writer.status != http.StatusPartialContent && header.Get("Content-Encoding") == "" &&
		(contentType == "application/json" || strings.HasSuffix(contentType, "+json") ||
			strings.HasPrefix(contentType, "text/")) && len(writer.buffer) >= minCompressedResponseBytes
	if compressible {
		select {
		case gzipSlots <- struct{}{}:
			compressor := gzipWriters.Get().(*gzip.Writer)
			compressor.Reset(writer.ResponseWriter)
			writer.compressed = compressor
			header.Set("Content-Encoding", "gzip")
			header.Del("Content-Length")
			if etag := header.Get("ETag"); strings.HasPrefix(etag, `"`) {
				header.Set("ETag", "W/"+etag)
			}
		default:
			// Compression must not become a source of queueing latency.
		}
	}
	writer.ResponseWriter.WriteHeader(writer.status)
	if len(writer.buffer) == 0 {
		return nil
	}
	var err error
	if writer.compressed != nil {
		_, err = writer.compressed.Write(writer.buffer)
	} else {
		_, err = writer.ResponseWriter.Write(writer.buffer)
	}
	writer.buffer = nil
	return err
}

func (writer *gzipResponseWriter) finish() {
	_ = writer.start()
	if writer.compressed != nil {
		_ = writer.compressed.Close()
		writer.compressed.Reset(io.Discard)
		gzipWriters.Put(writer.compressed)
		<-gzipSlots
	}
}
