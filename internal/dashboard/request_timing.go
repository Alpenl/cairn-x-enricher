package dashboard

import (
	"net/http"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// Timings stop at the first response header; body transfer to the browser is
// measured by its resource timing API. No response buffering is introduced.
func measureAPIRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api/") {
			next.ServeHTTP(writer, request)
			return
		}
		ctx, timing := cairn.WithRequestTiming(request.Context())
		measured := &timingResponseWriter{ResponseWriter: writer, timing: timing, started: time.Now()}
		next.ServeHTTP(measured, request.WithContext(ctx))
		if !measured.written {
			measured.WriteHeader(http.StatusOK)
		}
	})
}

type timingResponseWriter struct {
	http.ResponseWriter
	timing  *cairn.RequestTiming
	started time.Time
	written bool
}

func (writer *timingResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (writer *timingResponseWriter) WriteHeader(status int) {
	if !writer.written {
		writer.Header().Set("Server-Timing", writer.timing.Header(time.Since(writer.started)))
		writer.written = true
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *timingResponseWriter) Write(content []byte) (int, error) {
	if !writer.written {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(content)
}
