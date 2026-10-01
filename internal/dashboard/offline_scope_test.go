package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestOfflineScopeHeaderTracksNASCredentialOnErrorAndSuccess(t *testing.T) {
	first := cairn.NewClient("https://example.test", "first-fixture", http.DefaultClient)
	second := cairn.NewClient("https://example.test", "second-fixture", http.DefaultClient)
	if first.OfflineScope() == second.OfflineScope() {
		t.Fatal("credential change retained the same browser scope")
	}
	for _, backend := range []*cairn.Client{first, second} {
		server := &Server{backend: backend}
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusServiceUnavailable} {
			handler := server.withOfflineScope(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/4/reading", nil))
			value := recorder.Header().Get("X-Cairn-Offline-Scope")
			if value != backend.OfflineScope() || len(value) != 64 || strings.Contains(value, "fixture") {
				t.Fatal("scope header is absent, incorrect or includes credentials")
			}
		}
	}
}
