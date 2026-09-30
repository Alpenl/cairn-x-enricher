package localintegration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// Real Go HTTP client, Worker and local D1. Dropping the first source response
// exercises the exact replay; expiring the lease simulates a process exit.
func TestLocalWorkerRefreshCheckpointConsumesIntent(t *testing.T) {
	base := workerURL(t)
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("local D1 fixture configuration is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	id := createLink(t, base, "app")
	if _, err := queue.RefreshSourceWithOperation(ctx, id, "refresh-checkpoint-fixture"); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id || job.RefreshEpoch != 1 {
		t.Fatalf("refresh claim=%+v error=%v", job, err)
	}
	var lost atomic.Bool
	dropping := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(request.URL.Path, "/source") && lost.CompareAndSwap(false, true) {
			_ = response.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return response, nil
	})}
	checkpoint := cairn.NewClient(base, "internal", dropping)
	source := enrich.Source{OriginalText: "refreshed source text", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := checkpoint.SaveSource(ctx, id, job.LeaseToken, source); err != nil || !lost.Load() {
		t.Fatalf("lost-response source replay error=%v dropped=%t", err, lost.Load())
	}
	// A late failure ack from a previous process must be an idempotent no-op
	// after the checkpoint has consumed the matching refresh intent.
	if err := queue.AckSourceRefresh(ctx, id, job.RefreshEpoch, "failed", "snapshot unavailable"); err != nil {
		t.Fatalf("late refresh ack: %v", err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.Error != "" || detail.OriginalText != source.OriginalText {
		t.Fatalf("late ack changed saved refresh: detail=%+v error=%v", detail, err)
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths are supplied by this disposable local-integration harness.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-refreshcheckpoint", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	stored, err := restarted.GetSource(ctx, id)
	if err != nil || stored == nil || stored.OriginalText != source.OriginalText {
		t.Fatalf("stored source=%+v error=%v", stored, err)
	}
	next, err := restarted.Claim(ctx)
	if err != nil || next == nil || next.ID != id || next.RefreshEpoch != 0 {
		t.Fatalf("next claim repeated paid refresh: %+v error=%v", next, err)
	}
}

// The child exits after the Worker commits the source checkpoint but before
// SaveSource can receive its response. A new process must be able to replay
// that exact checkpoint without scheduling another refresh.
func TestLocalWorkerRefreshCheckpointSurvivesProcessExit(t *testing.T) {
	base := workerURL(t)
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("local D1 fixture configuration is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	id := createLink(t, base, "app")
	if _, err := queue.RefreshSourceWithOperation(ctx, id, "refresh-crash-fixture"); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id || job.RefreshEpoch != 1 {
		t.Fatalf("refresh claim=%+v error=%v", job, err)
	}
	//nolint:gosec // Re-executes this test binary with a fixed name and synthetic fixture values.
	crash := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerRefreshCheckpointCrashHelper$")
	crash.Env = append(os.Environ(), "CAIRN_REFRESH_CRASH_HELPER=1",
		"CAIRN_REFRESH_CRASH_ID="+strconv.FormatInt(id, 10),
		"CAIRN_REFRESH_CRASH_LEASE="+job.LeaseToken)
	output, err := crash.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "source committed before process exit") {
		t.Fatalf("source child exit=%v output=%s", err, string(output))
	}
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	source := enrich.Source{OriginalText: "refreshed source before process exit", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	stored, err := restarted.GetSource(ctx, id)
	if err != nil || stored == nil || stored.OriginalText != source.OriginalText {
		t.Fatalf("source after process exit=%+v error=%v", stored, err)
	}
	if err := restarted.SaveSource(ctx, id, job.LeaseToken, source); err != nil {
		t.Fatalf("same checkpoint replay after process exit: %v", err)
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths are supplied by this disposable local-integration harness.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-refreshcrash", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	next, err := restarted.Claim(ctx)
	if err != nil || next == nil || next.ID != id || next.RefreshEpoch != 0 {
		t.Fatalf("next claim repeated refresh after process exit: %+v error=%v", next, err)
	}
}

func TestLocalWorkerRefreshCheckpointCrashHelper(t *testing.T) {
	if os.Getenv("CAIRN_REFRESH_CRASH_HELPER") != "1" {
		return
	}
	id, err := strconv.ParseInt(os.Getenv("CAIRN_REFRESH_CRASH_ID"), 10, 64)
	if err != nil || id < 1 {
		t.Fatal("invalid child fixture link ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := cairn.NewClient(workerURL(t), "internal", &http.Client{Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := http.DefaultTransport.RoundTrip(request)
			if err != nil {
				return nil, err
			}
			if strings.HasSuffix(request.URL.Path, "/source") && response.StatusCode == http.StatusOK {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				_, _ = os.Stdout.WriteString("source committed before process exit\n")
				os.Exit(0)
			}
			return response, nil
		}),
	})
	source := enrich.Source{OriginalText: "refreshed source before process exit", OriginalLanguage: "en",
		RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := client.SaveSource(ctx, id, os.Getenv("CAIRN_REFRESH_CRASH_LEASE"), source); err != nil {
		t.Fatal(err)
	}
	t.Fatal("SaveSource returned without the injected process exit")
}
