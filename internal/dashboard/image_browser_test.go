package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// An explicit opt-in because ordinary Go gates must not require Chrome.
// The old backend is an HTTP contract fixture; the Go client/proxy/common.js
// and browser cache are real, with no Playwright request interception.
func TestBrowserPrivateImageCache(t *testing.T) {
	if os.Getenv("CAIRN_IMAGE_BROWSER") != "1" {
		t.Skip("set CAIRN_IMAGE_BROWSER=1 with Playwright and Chrome installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var deleted atomic.Bool
	var revision, imageCalls, legacyCalls atomic.Int32
	revision.Store(1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing fixture auth")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/enrichment/jobs/1":
			w.Header().Set("Content-Type", "application/json")
			if deleted.Load() {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":"not_found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":1,"url":"https://x.com/a/status/1","status":"completed","images":[]}`)
		case "/api/enrichment/images/enrichment/1/" + testImageKey:
			imageCalls.Add(1)
			// It continues serving bytes after deletion, like an old orphan R2 object.
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
			_, _ = fmt.Fprintf(w, "synthetic-image-%d", revision.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client := cairn.NewClient(upstream.URL, "fixture", upstream.Client())
	app := New(ctx, startedTracker(), client, &fakeProcessor{}, testLogger(), 1)
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/privacy-probe":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, `<!doctype html><script src="/assets/common.js"></script>`)
		case "/privacy-control":
			if r.Method == http.MethodPost {
				revision.Store(2)
				if r.URL.Query().Get("delete") == "1" {
					deleted.Store(true)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]int32{"images": imageCalls.Load(), "legacy": legacyCalls.Load()})
		default:
			// This branch only prewarms the exact URL used by older releases. It
			// intentionally models their old long-lived response before an upgrade.
			if r.URL.Path == "/api/images/enrichment/1/"+testImageKey && r.URL.RawQuery == "" {
				legacyCalls.Add(1)
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
				_, _ = io.WriteString(w, "old-cached-private-image")
				return
			}
			handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	// Fixed repository script and loopback test server, no shell/user command.
	command := exec.CommandContext(ctx, "node", "../../tests/browser/image-cache.mjs")
	command.Env = append(os.Environ(), "CAIRN_IMAGE_KEY=enrichment/1/"+testImageKey, "CAIRN_IMAGE_BASE="+server.URL)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("browser: %v", err)
	}
}
