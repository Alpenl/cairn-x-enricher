package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnconfiguredFormatterDoesNotQueuePaidWork(t *testing.T) {
	t.Parallel()
	s := &Server{}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/bookmarks/1/presentation", strings.NewReader(`{"force":false}`))
	r.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.presentation(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "formatting_unavailable") {
		t.Fatalf("unconfigured formatter accepted a job: %d %s", w.Code, w.Body.String())
	}
}
