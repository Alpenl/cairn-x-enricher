package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestMediaProxyPreservesRangesWithoutCompression(t *testing.T) {
	id := strings.Repeat("a", 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" || r.Header.Get("Range") != "bytes=4-7" || r.URL.Path != "/api/enrichment/media/"+id {
			t.Errorf("media request contract mismatch")
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 4-7/100")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(206)
		_, _ = io.WriteString(w, "ftyp")
	}))
	defer upstream.Close()
	s := &Server{backend: cairn.NewClient(upstream.URL, "private", upstream.Client())}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/media/{id}", s.mediaFile)
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequestWithContext(context.Background(), method, "/api/media/"+id, nil)
		r.Header.Set("Range", "bytes=4-7")
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		compressAPIResponses(mux).ServeHTTP(w, r)
		if w.Code != 206 || w.Header().Get("Content-Range") != "bytes 4-7/100" || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("invalid media response: %d %v", w.Code, w.Header())
		}
		if method == "GET" && w.Body.String() != "ftyp" || method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("invalid media bytes")
		}
	}
}
