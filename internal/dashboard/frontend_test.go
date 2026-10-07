package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestFrontendAssetsPassTheirChecks runs the dependency-free Node checker that
// CI also runs. It exercises the pure modules and the static deploy guards.
func TestFrontendAssetsPassTheirChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; CI runs this check explicitly")
	}
	script, err := filepath.Abs("frontend-check.mjs")
	if err != nil {
		t.Fatalf("resolve checker path: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("frontend checker is missing: %v", err)
	}

	// A hanging node process must not block the test run forever.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The binary is resolved from PATH and the script path is a fixed file in
	// this package, so there is no externally supplied argument here.
	//nolint:gosec // resolved node binary plus a repo-local fixed script path
	command := exec.CommandContext(ctx, node, script, ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("frontend checks failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "checks passed") {
		t.Fatalf("frontend checker did not report success:\n%s", output)
	}
}

var (
	shellAssetPattern  = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	moduleImportPatten = regexp.MustCompile(`from "\./([\w-]+\.js)"`)
)

// TestDashboardAssetsAreEmbedded fails if the shell or a module references a
// file the binary does not serve, which would otherwise surface only as a
// broken page in the browser after deploy.
func TestDashboardAssetsAreEmbedded(t *testing.T) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	handler := server.Handler()

	fetch := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		return response
	}

	wantTypes := map[string]string{".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml"}
	referenced := map[string]bool{}
	for _, match := range shellAssetPattern.FindAllStringSubmatch(string(appShell), -1) {
		referenced[match[1]] = true
	}
	if !referenced["/assets/js/main.js"] || !referenced["/assets/components.css"] {
		t.Fatalf("the shell does not load the application: %v", referenced)
	}
	for name, asset := range webAssets {
		if strings.HasPrefix(name, "js/") {
			for _, match := range moduleImportPatten.FindAllStringSubmatch(string(asset.content), -1) {
				referenced["/assets/js/"+match[1]] = true
			}
		}
	}
	for path := range referenced {
		response := fetch(path)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Errorf("GET %s = %d with %d bytes", path, response.Code, response.Body.Len())
			continue
		}
		if want := wantTypes[filepath.Ext(path)]; !strings.HasPrefix(response.Header().Get("Content-Type"), want) {
			t.Errorf("GET %s Content-Type = %q, want %s", path, response.Header().Get("Content-Type"), want)
		}
		if response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s is missing nosniff", path)
		}
	}
	for _, path := range []string{"/assets/missing.js", "/assets/../dashboard.go", "/assets/index.html"} {
		if response := fetch(path); response.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal", path)
		}
	}
}

// TestAssetsRevalidateWithETag keeps reloads cheap: an unchanged module is
// confirmed with a bodiless 304 instead of being downloaded again.
func TestAssetsRevalidateWithETag(t *testing.T) {
	server := New(context.Background(), startedTracker(), &fakeBackend{}, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/js/main.js", nil))
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("first GET = %d etag=%q cache=%q", first.Code, etag, first.Header().Get("Cache-Control"))
	}
	for _, header := range []string{etag, "W/" + etag, `"other", ` + etag} {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/js/main.js", nil)
		request.Header.Set("If-None-Match", header)
		second := httptest.NewRecorder()
		server.Handler().ServeHTTP(second, request)
		if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
			t.Fatalf("If-None-Match %q = %d with %d bytes", header, second.Code, second.Body.Len())
		}
	}
	stale := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/js/main.js", nil)
	stale.Header.Set("If-None-Match", `"stale"`)
	third := httptest.NewRecorder()
	server.Handler().ServeHTTP(third, stale)
	if third.Code != http.StatusOK || third.Body.Len() == 0 {
		t.Fatalf("stale ETag = %d", third.Code)
	}
}
