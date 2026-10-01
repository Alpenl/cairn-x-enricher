package dashboard

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimingPreservesCompressedResponse(t *testing.T) {
	content := strings.Repeat("test ", minCompressedResponseBytes)
	handler := measureAPIRequests(compressAPIResponses(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, content)
	})))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/fixture", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Server-Timing"), "nas;dur=") {
		t.Fatalf("missing response or timing: %d %v", recorder.Code, recorder.Header())
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != content {
		t.Fatalf("response was changed: %v", err)
	}
}
