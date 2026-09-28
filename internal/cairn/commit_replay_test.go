package cairn

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestExactSourceAndCompletionCommitsRetryOnlyAmbiguousFailures(t *testing.T) {
	for _, stage := range []string{"source", "complete"} {
		for _, first := range []string{"lost_response", "server_error", "conflict"} {
			t.Run(stage+"_"+first, func(t *testing.T) {
				var bodies [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer token" ||
						r.URL.Path != "/api/enrichment/jobs/7/"+stage {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodies = append(bodies, body)
					if len(bodies) == 1 {
						switch first {
						case "lost_response":
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = connection.Close()
							return
						case "server_error":
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = w.Write([]byte(`{"error":"temporary"}`))
							return
						case "conflict":
							w.WriteHeader(http.StatusConflict)
							_, _ = w.Write([]byte(`{"error":"lease_conflict"}`))
							return
						}
					}
					_, _ = w.Write([]byte(`{"id":7,"status":"completed"}`))
				}))
				defer server.Close()
				client := NewClient(server.URL, "token", server.Client())
				var err error
				if stage == "source" {
					err = client.SaveSource(context.Background(), 7, "same-lease", enrich.Source{
						OriginalText: "same saved content", Model: "fixture", RelatedLinks: []string{}, ImageURLs: []string{},
					})
				} else {
					err = client.Complete(context.Background(), 7, Completion{LeaseToken: "same-lease", Summary: "same result"})
				}
				if first == "conflict" {
					if err == nil || len(bodies) != 1 {
						t.Fatalf("conflict retried or accepted: error=%v calls=%d", err, len(bodies))
					}
					return
				}
				if err != nil || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
					t.Fatalf("exact commit retry: error=%v calls=%d identical=%v", err, len(bodies),
						len(bodies) == 2 && bytes.Equal(bodies[0], bodies[1]))
				}
			})
		}
	}
}
