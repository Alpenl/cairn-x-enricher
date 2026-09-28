package cairn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAggregateOverviewValidatesTheWholeCountSnapshot(t *testing.T) {
	valid := `{"version":1,"views":{"all":2,"inbox":1,"kept":1,"compiled":0,"drop":0,"uncertain":1},` +
		`"counts":{"total":2,"pending":1,"processing":0,"completed":0,"failed":0,"exhausted":0,"unsupported":1},` +
		`"attention":0,"queued":1}`
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/api/enrichment/overview" {
				t.Errorf("overview path = %q", request.URL.Path)
			}
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}))
	}
	server := serve(http.StatusOK, valid)
	result, err := NewClient(server.URL, "token", server.Client()).GetOverview(context.Background())
	server.Close()
	if err != nil || result.Views["uncertain"] != 1 || result.Counts.Unsupported != 1 {
		t.Fatalf("valid overview = %+v, %v", result, err)
	}
	for _, body := range []string{
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"kept":1,`, ``, 1),
		strings.Replace(valid, `"total":2`, `"total":3`, 1),
		strings.Replace(valid, `"queued":1`, `"queued":0`, 1),
	} {
		server := serve(http.StatusOK, body)
		_, err := NewClient(server.URL, "token", server.Client()).GetOverview(context.Background())
		server.Close()
		if err == nil {
			t.Fatalf("accepted inconsistent overview: %s", body)
		}
	}
	server = serve(http.StatusNotFound, `{"error":"not_found"}`)
	_, err = NewClient(server.URL, "token", server.Client()).GetOverview(context.Background())
	server.Close()
	if !errors.Is(err, ErrOverviewUnsupported) {
		t.Fatalf("old Worker error = %v", err)
	}
	server = serve(http.StatusServiceUnavailable, `{"error":"backend_error"}`)
	_, err = NewClient(server.URL, "token", server.Client()).GetOverview(context.Background())
	server.Close()
	if errors.Is(err, ErrOverviewUnsupported) || err == nil {
		t.Fatalf("temporary failure must not downgrade: %v", err)
	}
}
