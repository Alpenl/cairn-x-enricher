package localintegration

import (
	"context"
	"fmt"
	"io"
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
)

// A provider POST requires an acknowledged, first-use Worker reservation.
// Losing the reservation response after commit must not grant a second send.
func TestLocalWorkerProviderReservationResponseBoundaries(t *testing.T) {
	base := workerURL(t)
	shareRoot, configPath := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if shareRoot == "" || configPath == "" {
		t.Fatal("local D1 fixture configuration is missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var posts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if err := queue.VerifySourceLeaseCapability(ctx); err != nil {
		t.Fatal(err)
	}
	catalog, _, err := queue.GetClassificationCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := createLink(t, base, "app")
	job, err := queue.Claim(ctx)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("claim=%+v error=%v", job, err)
	}
	if err := queue.AdmitSourceStage(ctx, id, job.LeaseToken, "fetch", 210*time.Second); err != nil {
		t.Fatal(err)
	}
	input := enrich.Input{ID: id, URL: job.URL, Attempt: job.Attempt,
		LeaseToken: job.LeaseToken, ContentRevision: job.ContentRevision, MinRemainingMS: 210_000}
	modelWithLedger := func(ledger *cairn.Client) *enrich.ResponsesClient {
		model := enrich.NewResponsesClient(provider.URL, "fixture-key", "grok-test", 1024, "",
			&http.Client{Timeout: 10 * time.Second}, catalog)
		model.SetPaidAttemptLedger(ledger)
		return model
	}
	// The request never reaches Worker: no durable permit and no provider POST.
	before := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/api/enrichment/provider-attempts/reserve" {
				return nil, io.ErrUnexpectedEOF
			}
			return http.DefaultTransport.RoundTrip(request)
		}),
	})
	if _, err := modelWithLedger(before).FetchSource(ctx, input); err == nil || posts.Load() != 0 {
		t.Fatalf("precommit reservation failure sent a provider POST: posts=%d error=%v", posts.Load(), err)
	}
	if attempts := readProviderAttempts(ctx, t, base, "internal"); len(attempts) != 0 {
		t.Fatalf("precommit failure left a provider reservation: %+v", attempts)
	}
	// Worker commits the reservation. The transport discards its successful
	// response before the Go adapter can receive the one-use send permission.
	var reserveCalls atomic.Int32
	after := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/api/enrichment/provider-attempts/reserve" {
				reserveCalls.Add(1)
				response, err := http.DefaultTransport.RoundTrip(request)
				if err != nil {
					return nil, err
				}
				if response.StatusCode != http.StatusOK {
					return response, nil
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				return nil, io.ErrUnexpectedEOF
			}
			return http.DefaultTransport.RoundTrip(request)
		}),
	})
	if _, err := modelWithLedger(after).FetchSource(ctx, input); err == nil ||
		reserveCalls.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("committed reservation response loss: reserve calls=%d posts=%d error=%v",
			reserveCalls.Load(), posts.Load(), err)
	}
	attempts := readProviderAttempts(ctx, t, base, "internal")
	if len(attempts) != 1 || attempts[0].LinkID != id || attempts[0].Stage != "fetch" ||
		attempts[0].State != "reserved" || attempts[0].ResponseID != "" {
		t.Fatalf("committed reservation was not retained: %+v", attempts)
	}
	// A new client repeats the same model operation under the same lease. The
	// Worker recognizes it but cannot issue a second send permission.
	if _, err := modelWithLedger(queue).FetchSource(ctx, input); err == nil || posts.Load() != 0 {
		t.Fatalf("same-operation replay sent a provider POST: posts=%d error=%v", posts.Load(), err)
	}
	operator := cairn.NewClient(base, "operator", &http.Client{Timeout: 10 * time.Second})
	unknown, err := operator.InspectProviderAttempt(ctx, attempts[0].OperationKey)
	if err != nil || unknown.State != "reserved" || unknown.CurrentPaidUnresolved == nil ||
		*unknown.CurrentPaidUnresolved != 1 {
		t.Fatalf("lost-reservation inspection=%+v error=%v", unknown, err)
	}
	wrangler := filepath.Join(shareRoot, "worker", "node_modules", ".bin", "wrangler")
	//nolint:gosec // Wrangler and config paths come from this isolated local-integration harness.
	command := exec.CommandContext(ctx, wrangler, "d1", "execute", "cairn-share-providerreserve", "--local",
		"--config", configPath, "--command",
		fmt.Sprintf("UPDATE links SET enrichment_lease_until='2000-01-01T00:00:00.000Z' WHERE id=%d", id))
	command.Dir = filepath.Join(shareRoot, "worker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("expire local D1 lease: %v: %s", err, strings.TrimSpace(string(output)))
	}
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	if claim, err := restarted.Claim(ctx); err != nil || claim != nil || posts.Load() != 0 {
		t.Fatalf("unknown reservation was reclaimed or sent: claim=%+v posts=%d error=%v",
			claim, posts.Load(), err)
	}
}
