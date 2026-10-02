package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestImageAndThumbnailTimingIsAvailableBeforeDeferredClose(t *testing.T) {
	image := thumbnailFixture(t, 320, 240, true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/enrichment/jobs/1") {
			_, _ = io.WriteString(w, `{"id":1,"url":"https://x.com/a/status/1","status":"completed","images":[]}`)
			return
		}
		w.Header().Set("X-Cairn-Image-Privacy", "1")
		w.Header().Set("ETag", `"image-v1"`)
		w.Header().Set("Server-Timing", `total;dur=9,db;dur=2,r2;dur=3,sql-count;desc="2",r2-calls;desc="1"`)
		if r.Header.Get("If-None-Match") == `"image-v1"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(image)
	}))
	defer upstream.Close()
	client := cairn.NewClient(upstream.URL, "fixture", upstream.Client())
	app := New(t.Context(), startedTracker(), client, &fakeProcessor{}, testLogger(), 1)
	defer app.Drain(time.Second)
	for _, query := range []string{"", "?size=160", "?size=160"} {
		response := thumbnailRequest(t, app, query)
		if response.Code != 200 {
			t.Fatalf("image/thumbnail: %d %s", response.Code, response.Body)
		}
		header := response.Result().Header.Get("Server-Timing")
		for _, expected := range []string{"worker;dur=9.00", "d1;dur=2.00", "r2;dur=3.00", `r2-calls;desc="1"`} {
			if !strings.Contains(header, expected) {
				t.Fatalf("missing timing %s: %s", expected, header)
			}
		}
		if strings.Contains(header, `upstream_calls;desc="0"`) {
			t.Fatalf("image upstream disappeared before headers: %s", header)
		}
	}
}
