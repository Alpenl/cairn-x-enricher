package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type modernCatalogFixture struct {
	fakeBackend
	load func(context.Context) (cairn.V2Taxonomy, error)
}

type observedWaitContext struct {
	context.Context
	ready chan struct{}
}

func (c observedWaitContext) Done() <-chan struct{} {
	select {
	case c.ready <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func (b *modernCatalogFixture) GetV2Taxonomy(ctx context.Context) (cairn.V2Taxonomy, error) {
	return b.load(ctx)
}

func TestModernTaxonomyRevisionInvalidationDoesNotWaitForOrRefillOldFlight(t *testing.T) {
	var calls atomic.Int32
	var failed atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	source := &modernCatalogFixture{load: func(ctx context.Context) (cairn.V2Taxonomy, error) {
		call := calls.Add(1)
		if failed.Load() {
			return cairn.V2Taxonomy{}, errors.New("metadata unavailable")
		}
		label := "renamed"
		revision := 2
		if call == 1 {
			label = "original"
			revision = 1
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return cairn.V2Taxonomy{}, ctx.Err()
			}
		}
		return cairn.V2Taxonomy{Version: "fixed-definition", Topics: []cairn.TaxonomyTerm{{ID: "portrait", Label: label, DisplayRevision: revision}}}, nil
	}}
	cache := newTaxonomyCache(source)
	old := make(chan cairn.V2Taxonomy, 1)
	go func() { value, _, _ := cache.Modern(t.Context()); old <- value }()
	<-started
	cache.Invalidate()
	current, stale, err := cache.Modern(t.Context())
	if err != nil || stale || current.Topics[0].Label != "renamed" {
		t.Fatalf("new revision: %v %v %v", current, stale, err)
	}
	close(release)
	if value := <-old; value.Topics[0].Label != "original" {
		t.Fatal("old waiter lost its snapshot")
	}
	current, _, err = cache.Modern(t.Context())
	if err != nil || current.Topics[0].DisplayRevision != 2 || calls.Load() != 2 {
		t.Fatal("old flight refilled modern revision")
	}
	failed.Store(true)
	cache.Invalidate()
	for range 3 {
		value, stale, err := cache.Modern(t.Context())
		if err != nil || !stale || value.Topics[0].Label != "renamed" {
			t.Fatal("outage lost last good metadata")
		}
	}
	if calls.Load() != 3 {
		t.Fatal("metadata outage retried on every caller")
	}
}

func TestSnapshotColdFailureBackoffBoundsSequentialRetries(t *testing.T) {
	var cache snapshotCache[int]
	var calls atomic.Int32
	load := func(context.Context) (int, error) { calls.Add(1); return 0, errors.New("cold failure") }
	for range 10 {
		if _, _, err := cache.read(t.Context(), t.Context(), time.Minute, load); err == nil {
			t.Fatal("cold failure hidden")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("failure calls=%d", calls.Load())
	}
}

func TestSnapshotSharedTimingSurvivesCanceledLeaderAndWarmHit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		w.Header().Set("Server-Timing", `total;dur=5,db;dur=2`)
		_ = json.NewEncoder(w).Encode(cairn.BookmarkOverview{Version: 1, Views: map[string]int{"all": 0, "inbox": 0, "kept": 0, "compiled": 0, "drop": 0, "uncertain": 0}})
	}))
	defer upstream.Close()
	client := cairn.NewClient(upstream.URL, "fixture", upstream.Client())
	var cache snapshotCache[cairn.BookmarkOverview]
	leader, cancel := context.WithCancel(t.Context())
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := cache.read(leader, t.Context(), time.Minute, client.GetOverview)
		leaderDone <- err
	}()
	<-started
	cancel()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader cancellation: %v", err)
	}
	ctx, timing := cairn.WithRequestTiming(t.Context())
	joined := observedWaitContext{Context: ctx, ready: make(chan struct{}, 1)}
	followerDone := make(chan error, 1)
	go func() {
		_, _, err := cache.read(joined, t.Context(), time.Minute, client.GetOverview)
		followerDone <- err
	}()
	<-joined.ready
	close(release)
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
	header := timing.Header(time.Millisecond)
	if !strings.Contains(header, `cache;desc="shared"`) || !strings.Contains(header, `upstream_calls;desc="1"`) || !strings.Contains(header, "worker;dur=5.00") {
		t.Fatalf("shared work unaccounted: %s", header)
	}
	warm, warmTiming := cairn.WithRequestTiming(t.Context())
	_, _, err := cache.read(warm, t.Context(), time.Minute, client.GetOverview)
	if err != nil || calls.Load() != 1 || !strings.Contains(warmTiming.Header(0), `cache;desc="hit"`) {
		t.Fatal("warm cache accounting or cancellation lifetime lost")
	}
}

type aggregateBackstageFixture struct {
	fakeBackend
	aggregate cairn.BookmarkBackstage
	err       error
	calls     atomic.Int32
}

func (b *aggregateBackstageFixture) GetBackstage(context.Context) (cairn.BookmarkBackstage, error) {
	b.calls.Add(1)
	return b.aggregate, b.err
}

func TestBackstageAggregateRemovesListFanoutAndCarriesOverview(t *testing.T) {
	backend := &aggregateBackstageFixture{aggregate: cairn.BookmarkBackstage{Attention: []cairn.Bookmark{}, Overview: cairn.BookmarkOverview{Views: map[string]int{"all": 0}}}}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	for range 2 {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/backstage", nil))
		var result backstageSummary
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Overview == nil {
			t.Fatalf("aggregate response: %d %s", response.Code, response.Body)
		}
	}
	if backend.calls.Load() != 1 || backend.listCalls != 0 {
		t.Fatal("aggregate still fanned out to attention lists")
	}
}

func TestBackstageOldWorkerFallbackUsesSummaryRows(t *testing.T) {
	backend := &aggregateBackstageFixture{err: cairn.ErrBackstageUnsupported}
	server := New(t.Context(), startedTracker(), backend, &fakeProcessor{}, testLogger(), 1)
	defer server.Drain(time.Second)
	if _, err := server.computeBackstageSummary(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.listCalls != 2 || !backend.query.SummaryOnly {
		t.Fatal("legacy fallback transferred full articles")
	}
	backend.err = errors.New("backend unavailable")
	backend.listCalls = 0
	if _, err := server.computeBackstageSummary(t.Context()); err == nil || backend.listCalls != 0 {
		t.Fatal("real aggregate failure hidden with more queries")
	}
}
