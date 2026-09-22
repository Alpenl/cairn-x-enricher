package cairn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestImageRequiresLiveOwnerBeforeAndAfterRead(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
		wrongID       bool
	}{
		{"live", 200, 200, false},
		{"deleted_before", 404, 200, false},
		{"deleted_during", 200, 404, false},
		{"unauthorized_before", 401, 200, false},
		{"unavailable_after", 200, 503, false},
		{"wrong_owner", 200, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var details, images atomic.Int32
			var imageClosed atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing synthetic internal authorization")
				}
				if r.URL.Path == "/api/enrichment/jobs/7" {
					status := tc.before
					if details.Add(1) > 1 {
						status = tc.after
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status != 200 {
						_, _ = io.WriteString(w, `{ "error": "not_found" }`)
						return
					}
					id := 7
					if tc.wrongID {
						id = 8
					}
					_, _ = fmt.Fprintf(w, `{ "id": %d, "url": "https://x.com/a/status/1", "status": "completed", "images": [] }`, id)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/enrichment/images/") {
					images.Add(1)
					// Deliberately model an old Worker that still serves the orphan object.
					w.Header().Set("Content-Type", "image/jpeg")
					_, _ = io.WriteString(w, "private-image")
					return
				}
				http.NotFound(w, r)
			}))
			defer upstream.Close()
			httpClient := upstream.Client()
			transport := httpClient.Transport
			httpClient.Transport = imagePrivacyTransport(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if err == nil && strings.HasPrefix(request.URL.Path, "/api/enrichment/images/") {
					response.Body = &imagePrivacyBody{ReadCloser: response.Body, closed: &imageClosed}
				}
				return response, err
			})
			response, err := NewClient(upstream.URL, "fixture", httpClient).GetImage(context.Background(), "enrichment/7/"+strings.Repeat("a", 64)+".jpg")
			if response != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if tc.name == "live" {
				if err != nil || response == nil {
					t.Fatalf("live image: %v", err)
				}
				body, readErr := io.ReadAll(response.Body)
				if readErr != nil || string(body) != "private-image" || details.Load() != 2 || images.Load() != 1 {
					t.Fatalf("image or owner checks missing: details=%d images=%d", details.Load(), images.Load())
				}
				return
			}
			if err == nil || response != nil {
				t.Fatalf("private image escaped after owner rejection: response=%v err=%v", response != nil, err)
			}
			if tc.before != 200 || tc.wrongID {
				if images.Load() != 0 {
					t.Fatal("image fetched before validating owner")
				}
			} else if images.Load() != 1 || details.Load() != 2 {
				t.Fatal("post-read owner check missing")
			}
			if images.Load() > 0 && !imageClosed.Load() {
				t.Fatal("rejected response body was not closed")
			}
			if !tc.wrongID {
				var api *APIError
				if !errors.As(err, &api) {
					t.Fatalf("lost typed backend error: %v", err)
				}
			}
		})
	}
}

type imagePrivacyTransport func(*http.Request) (*http.Response, error)

func (f imagePrivacyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type imagePrivacyBody struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (b *imagePrivacyBody) Close() error { b.closed.Store(true); return b.ReadCloser.Close() }
