package cairn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadingSnapshotRejectsCrossRevisionState(t *testing.T) {
	body := `{"version":1,"detail":{"id":7,"url":"https://x.com/a/status/7","status":"completed",` +
		`"original_text":"private text","cache_identity":{"schema_version":1,"content_revision":2,"body_revision":1,` +
		`"personal_revision":3,"latest_decision_id":4,"latest_entity_revision":1}},` +
		`"selection":{"id":7,"revision":3,"available":true,"selection":{"topics":["llm"],` +
		`"content_functions":[],"carriers":[],"affordances":[],"form":"","use":""},"state":{"version":1}},` +
		`"entities":{"id":7,"revision":3,"state":"completed_empty","entities":[]}}`
	serve := func(payload string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/enrichment/jobs/7/reading" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(payload))
		}))
	}
	server := serve(body, http.StatusOK)
	reading, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
	server.Close()
	if err != nil || reading.Detail.OriginalText != "private text" ||
		!reading.Selection.Available || reading.Selection.Revision != 3 {
		t.Fatalf("valid reading = %+v, %v", reading, err)
	}
	for _, payload := range []string{
		strings.Replace(body, `"revision":3,"available"`, `"revision":2,"available"`, 1),
		strings.Replace(body, `"revision":3,"state":"completed_empty"`, `"revision":2,"state":"completed_empty"`, 1),
	} {
		server := serve(payload, http.StatusOK)
		_, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
		server.Close()
		if err == nil {
			t.Fatal("cross-revision reading must be rejected")
		}
	}
	server = serve(`{"error":"not_found"}`, http.StatusNotFound)
	_, err = NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
	server.Close()
	if !errors.Is(err, ErrV2Unsupported) {
		t.Fatalf("old Worker must be recognizable: %v", err)
	}
}
