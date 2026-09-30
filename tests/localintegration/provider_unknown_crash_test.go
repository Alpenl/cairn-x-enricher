package localintegration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// The provider receives the complete paid POST, but the Go process exits
// before any response arrives. The durable reservation must block a new send.
func TestLocalWorkerUnknownProviderResultSurvivesProcessKill(t *testing.T) {
	base := workerURL(t)
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("local D1 fixture configuration is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var posts atomic.Int32
	postReceived := make(chan struct{}, 1)
	releaseProvider := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/responses" {
			t.Errorf("unexpected provider request: %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Errorf("read complete provider POST: %v", err)
			return
		}
		posts.Add(1)
		select {
		case postReceived <- struct{}{}:
		default:
		}
		select {
		case <-request.Context().Done():
		case <-releaseProvider:
		}
	}))
	defer provider.Close()
	defer close(releaseProvider)
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	id := createLink(t, base, "app")
	//nolint:gosec // Re-executes this test binary against an isolated local provider and Worker.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerUnknownProviderCrashHelper$")
	child.Env = append(os.Environ(), "CAIRN_PROVIDER_UNKNOWN_HELPER=1", "CAIRN_PROVIDER_URL="+provider.URL)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-postReceived:
	case <-ctx.Done():
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("provider did not receive the POST before timeout: %s", output.String())
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill paid-call process: %v", err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("paid-call process exited cleanly instead of being killed")
	}
	if posts.Load() != 1 {
		t.Fatalf("provider POST count after process kill=%d, want 1", posts.Load())
	}
	items := readProviderAttempts(ctx, t, base, "internal")
	var operationKey string
	for _, item := range items {
		if item.LinkID == id {
			if item.Stage != "fetch" || item.State != "reserved" || item.ResponseID != "" {
				t.Fatalf("unknown paid attempt after process kill=%+v", item)
			}
			operationKey = item.OperationKey
		}
	}
	if operationKey == "" {
		t.Fatal("paid attempt was not reserved before the provider POST")
	}
	operator := cairn.NewClient(base, "operator", &http.Client{Timeout: 10 * time.Second})
	unknown, err := operator.InspectProviderAttempt(ctx, operationKey)
	if err != nil || unknown.CurrentPaidUnresolved == nil || *unknown.CurrentPaidUnresolved != 1 ||
		unknown.State != "reserved" || unknown.ResponseID != nil {
		t.Fatalf("unknown paid attempt inspection=%+v error=%v", unknown, err)
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths come from this isolated local-integration harness.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-providerunknown", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if claim, err := restarted.Claim(ctx); err != nil || claim != nil || posts.Load() != 1 {
		t.Fatalf("unknown result was reclaimed or resent: claim=%+v posts=%d error=%v", claim, posts.Load(), err)
	}
}

func TestLocalWorkerUnknownProviderCrashHelper(t *testing.T) {
	if os.Getenv("CAIRN_PROVIDER_UNKNOWN_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	queue := cairn.NewClient(workerURL(t), "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	catalog, _, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := enrich.NewResponsesClient(os.Getenv("CAIRN_PROVIDER_URL"), "fixture-key", "grok-test",
		1024, "", &http.Client{Timeout: 20 * time.Second}, catalog)
	model.SetPaidAttemptLedger(queue)
	worker := processor.NewStaged(queue, model, nil, "", "", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	worker.SetPaidStageTimeout(20 * time.Second)
	job, err := queue.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim paid source job=%+v error=%v", job, err)
	}
	if err := worker.Process(ctx, job); err != nil {
		t.Fatal(err)
	}
	t.Fatal("provider returned while the fixture withheld its response")
}
