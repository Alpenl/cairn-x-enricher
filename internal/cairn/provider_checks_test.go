package cairn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderCheckStatusUsesBodyFreeGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
			t.Errorf("unexpected status request: %s length=%d", r.Method, r.ContentLength)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"waiting","next_check_at":100,"last_success_at":0,"valid_until":0,"reason":"timeout","failures":1,"manual_after":0,"can_recover":true}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "test", server.Client())
	status, err := client.ProviderCheck(context.Background(), strings.Repeat("a", 64), "status", nil)
	if err != nil || status.State != "waiting" || status.Failures != 1 {
		t.Fatalf("status=%+v error=%v", status, err)
	}
}
