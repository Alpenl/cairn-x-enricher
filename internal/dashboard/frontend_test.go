package dashboard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFrontendAssetsPassTheirChecks runs the dependency-free Node checker that
// CI also runs. It guards the dashboard behaviour the performance work depends
// on, such as cached formatters and chunked exports.
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

// TestDashboardAssetsAreEmbedded fails if a page references an asset that is
// not served, which would surface only as a broken page in the browser.
func TestDashboardAssetsAreEmbedded(t *testing.T) {
	assets := map[string][]byte{
		"dashboard.css": dashboardCSS,
		"common.js":     commonJS,
		"home.js":       homeJS,
		"reader.js":     readerJS,
		"backstage.js":  backstageJS,
		"download.svg":  downloadSVG,
	}
	for name, content := range assets {
		if len(content) == 0 {
			t.Errorf("embedded asset %s is empty", name)
		}
	}
	for name, page := range map[string][]byte{"index": indexHTML, "reader": readerHTML, "backstage": backstageHTML} {
		text := string(page)
		for _, want := range []string{"/assets/dashboard.css", "/assets/common.js"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s.html does not reference %s", name, want)
			}
		}
	}
}
