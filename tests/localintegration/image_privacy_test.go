package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

func TestLocalWorkerPrivateImageLifecycle(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The harness seeds only an R2 object. Creation, current image registration
	// and deletion use authenticated HTTP and the actual Worker and D1.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/links", strings.NewReader(`{"url":"https://x.com/image/status/1"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&created)
	_ = response.Body.Close()
	if response.StatusCode != 201 || decodeErr != nil || created.ID != 1 {
		t.Fatalf("create status=%d id=%d err=%v", response.StatusCode, created.ID, decodeErr)
	}
	seedArchivedOriginal(ctx, t, created.ID, "Fixture original text")
	client := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	app := dashboard.New(ctx, health.NewTracker(), client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	proxy := httptest.NewServer(app.Handler())
	defer proxy.Close()
	imageURL := proxy.URL + "/api/images/enrichment/1/" + strings.Repeat("a", 64) + ".png?privacy=1"
	get := func(want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("If-None-Match", `"old-image"`)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
			t.Fatalf("image status=%d cache=%q", resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
		return body
	}
	// A live owner alone does not authorize an unreferenced historical object.
	// Register the fixture through the real completion route, which verifies
	// the R2 object and commits its current image membership atomically.
	get(404)
	job, err := client.ClaimByID(ctx, created.ID)
	if err != nil || job == nil {
		t.Fatalf("claim image fixture: job=%v err=%v", job, err)
	}
	completion := cairn.Completion{LeaseToken: job.LeaseToken, AITitle: "Image privacy fixture",
		OriginalLanguage: "en", OriginalText: "Synthetic image source", TranslatedText: "图片隐私测试",
		Summary: "Synthetic image reading aid", Model: "manual", RelatedLinks: []string{},
		Images: []cairn.ImageRef{{Key: "enrichment/1/" + strings.Repeat("a", 64) + ".png", ContentType: "image/png"}}}
	if err := client.Complete(ctx, created.ID, completion); err != nil {
		t.Fatalf("register current image: %v", err)
	}
	body := get(200)
	if !bytes.HasPrefix(body, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatal("actual R2 image missing PNG signature")
	}
	deletion, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/links/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	deletion.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
	deleted, err := http.DefaultClient.Do(deletion)
	if err != nil {
		t.Fatal(err)
	}
	_ = deleted.Body.Close()
	if deleted.StatusCode != 204 {
		t.Fatalf("delete %d", deleted.StatusCode)
	}
	if bytes.Contains(get(404), []byte{137, 80, 78, 71}) {
		t.Fatal("deleted image exposed")
	}
	t.Log("actual Worker/D1/R2 + Go client/dashboard: PNG read, no-store, authenticated delete, conditional stale read rejected; zero model calls")
}
