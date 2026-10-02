package cairn

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConditionalImagePrivacyCapabilityAndLegacyDeletionFallback(t *testing.T) {
	for _, mode := range []string{"modern", "legacy_live", "legacy_deleted_before", "legacy_deleted_during"} {
		t.Run(mode, func(t *testing.T) {
			var images, owners atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing internal authorization")
				}
				if strings.HasPrefix(r.URL.Path, "/api/enrichment/jobs/7") {
					count := owners.Add(1)
					if mode == "legacy_deleted_before" || mode == "legacy_deleted_during" && count == 2 {
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `{"error":"not_found"}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":7,"url":"https://x.com/a/status/1","status":"completed","images":[]}`)
					return
				}
				images.Add(1)
				if r.Header.Get("X-Cairn-Image-Privacy") != "1" {
					t.Error("image capability missing")
				}
				if mode == "modern" {
					w.Header().Set("X-Cairn-Image-Privacy", "1")
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("ETag", `"source-v1"`)
				_, _ = io.WriteString(w, "private-image")
			}))
			defer upstream.Close()
			client := NewClient(upstream.URL, "fixture", upstream.Client())
			response, err := client.GetImageConditional(t.Context(), "enrichment/7/"+strings.Repeat("a", 64)+".jpg", "")
			deleted := strings.Contains(mode, "deleted")
			if deleted {
				if err == nil || response != nil {
					t.Fatal("deleted owner image escaped fallback")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || string(body) != "private-image" {
					t.Fatalf("body: %q, %v", body, readErr)
				}
			}
			if mode == "modern" {
				response, err := client.GetImageConditional(t.Context(), "enrichment/7/"+strings.Repeat("a", 64)+".jpg", "")
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if images.Load() != 2 || owners.Load() != 1 {
					t.Fatal("verified image still fetched full bookmark")
				}
			}
			if mode == "legacy_live" && (images.Load() != 1 || owners.Load() != 2) {
				t.Fatal("legacy pre/post owner checks lost")
			}
			if mode == "legacy_deleted_before" && images.Load() != 0 {
				t.Fatal("legacy served read fetched after owner denial")
			}
		})
	}
}

func TestConditionalImage304RequiresExactStrongValidator(t *testing.T) {
	for _, returned := range []string{`"v1"`, `"v2"`, ""} {
		t.Run(returned, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/enrichment/jobs/7") {
					_, _ = io.WriteString(w, `{"id":7,"url":"https://x.com/a/status/1","status":"completed","images":[]}`)
					return
				}
				if r.Header.Get("If-None-Match") != `"v1"` {
					t.Error("conditional validator missing")
				}
				w.Header().Set("X-Cairn-Image-Privacy", "1")
				w.Header().Set("ETag", returned)
				w.WriteHeader(http.StatusNotModified)
			}))
			defer server.Close()
			response, err := NewClient(server.URL, "fixture", server.Client()).GetImageConditional(t.Context(), "enrichment/7/"+strings.Repeat("a", 64)+".jpg", `"v1"`)
			if returned == `"v1"` {
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
			} else if err == nil {
				t.Fatal("unverified 304 accepted")
			}
		})
	}
}

func TestImageCapabilityRollbackNeverExposesUnacknowledgedOrphan(t *testing.T) {
	var old, deleted atomic.Bool
	var images atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/enrichment/jobs/7") {
			if deleted.Load() {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":"not_found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":7,"url":"https://x.com/a/status/1","status":"completed","images":[]}`)
			return
		}
		images.Add(1)
		if !old.Load() {
			w.Header().Set("X-Cairn-Image-Privacy", "1")
		}
		_, _ = io.WriteString(w, "orphan")
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	key := "enrichment/7/" + strings.Repeat("a", 64) + ".jpg"
	response, err := client.GetImageConditional(t.Context(), key, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	old.Store(true)
	deleted.Store(true)
	response, err = client.GetImageConditional(t.Context(), key, "")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || response != nil {
		t.Fatal("rollback exposed unacknowledged orphan")
	}
	response, err = client.GetImageConditional(t.Context(), key, "")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || response != nil || images.Load() != 2 {
		t.Fatal("rollback failed to restore pre-read owner checks")
	}
}

func TestTimingRelaysOnlyNumericCountersAndAttributesSharedWork(t *testing.T) {
	_, shared := WithRequestTiming(t.Context())
	shared.record(9*time.Millisecond, `total;dur=5,db;dur=2,r2;dur=1,sql-count;desc="3",db-round-trips;desc="1",rows-read;desc="12",rows-read-unknown;desc="2",r2-calls;desc="1",sql-count;desc="secret",rows-read;desc="NaN",r2-calls;desc="-1"`)
	ctx, waiter := WithRequestTiming(t.Context())
	RecordCacheTiming(ctx, "shared", 4*time.Millisecond, 10*time.Millisecond, shared)
	header := waiter.Header(6 * time.Millisecond)
	for _, wanted := range []string{`upstream_calls;desc="1"`, `cache;desc="shared"`, "cache_wait;dur=4.00", "cache_load;dur=10.00", `sql-count;desc="3"`, `rows-read-unknown;desc="2"`, `r2-calls;desc="1"`} {
		if !strings.Contains(header, wanted) {
			t.Fatalf("missing %s: %s", wanted, header)
		}
	}
	if strings.Contains(header, "secret") || strings.Contains(header, "NaN") {
		t.Fatal("non-numeric metadata relayed")
	}
}

func TestBackstageNegotiationAndSummaryValidation(t *testing.T) {
	base := BookmarkBackstage{Version: 1, Attention: []Bookmark{{ID: 7, URL: "https://x.com/a/status/1", Status: "failed"}}, AttentionTotal: 1,
		Counts: BookmarkCounts{Total: 1, Failed: 1}, Overview: BookmarkOverview{Version: 1, Views: map[string]int{"all": 1, "inbox": 1, "kept": 0, "compiled": 0, "drop": 0, "uncertain": 0}, Counts: BookmarkCounts{Total: 1, Failed: 1}, Attention: 1}}
	for _, mode := range []string{"good", "old", "body", "counts", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Cairn-Backstage") != "1" {
					t.Error("missing capability")
				}
				if mode != "old" {
					w.Header().Set("X-Cairn-Backstage", "1")
				}
				result := base
				result.Attention = append([]Bookmark(nil), base.Attention...)
				if mode == "body" {
					result.Attention[0].OriginalText = "unneeded body"
				}
				if mode == "counts" {
					result.Counts.Total = 2
				}
				body, _ := json.Marshal(result)
				if mode == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"unexpected":true}`)...)
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "fixture", server.Client()).GetBackstage(context.Background())
			if (err == nil) != (mode == "good") {
				t.Fatalf("mode %s: %v", mode, err)
			}
		})
	}
}
