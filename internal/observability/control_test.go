package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlRejectsRemoteAndCrossOriginWrites(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "policy.json"), 0)
	if err != nil {
		t.Fatal(err)
	}
	request := func(peer, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "http://127.0.0.1:9090/v1/observability/logs",
			strings.NewReader(`{"expected_version":0,"mode":"off"}`))
		r.RemoteAddr = peer
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		store.Handler().ServeHTTP(response, r)
		return response
	}
	if got := request("192.0.2.10:1234", ""); got.Code != http.StatusForbidden {
		t.Fatalf("remote caller = %d", got.Code)
	}
	if got := request("127.0.0.1:1234", "http://attacker.invalid"); got.Code != http.StatusForbidden {
		t.Fatalf("cross-origin caller = %d", got.Code)
	}
	if got := request("127.0.0.1:1234", ""); got.Code != http.StatusOK {
		t.Fatalf("local command = %d: %s", got.Code, got.Body.String())
	}
	if got := request("127.0.0.1:1234", ""); got.Code != http.StatusConflict {
		t.Fatalf("stale version = %d: %s", got.Code, got.Body.String())
	}
}
