package cairn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientClaimsOnlyAllowedSourceStageAndDefersUnusedLease(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer internal" ||
			r.Header.Get("X-Cairn-Source-Component-Gate") != "1" && r.URL.Path == "/api/enrichment/jobs/claim" {
			t.Errorf("missing source capability headers on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/enrichment/jobs/claim":
			if r.Header.Get("X-Cairn-Source-Stage-Pause") != "1" {
				t.Error("claim omitted stage-pause response capability")
			}
			if r.Header.Get("X-Cairn-Source-Stage-Mask") != "reading" {
				t.Errorf("claim mask = %q", r.Header.Get("X-Cairn-Source-Stage-Mask"))
			}
			_ = json.NewEncoder(w).Encode(Job{ID: 7, URL: "https://x.com/u/status/7",
				Attempt: 1, LeaseToken: "lease-7", LeaseUntil: "2026-09-29T12:00:00Z",
				ContentRevision: 1, SourceComponent: "reading"})
		case "/api/enrichment/jobs/7/local-defer":
			if r.Header.Get("X-Cairn-Provider-Attempt-Ledger") != "1" {
				t.Error("local deferral omitted paid-attempt ledger capability")
			}
			var body struct {
				LeaseToken string `json:"lease_token"`
				Stage      string `json:"stage"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
				body.LeaseToken != "lease-7" || body.Stage != "reading" {
				t.Errorf("local deferral body = %+v, %v", body, err)
			}
			_, _ = w.Write([]byte(`{"id":7,"status":"deferred"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "internal", server.Client())
	if job, err := client.ClaimAllowed(context.Background(), false, false); job != nil || err != nil || requests != 0 {
		t.Fatalf("both stages paused but claim sent: job=%+v err=%v requests=%d", job, err, requests)
	}
	job, err := client.ClaimAllowed(context.Background(), false, true)
	if err != nil || job == nil || job.ID != 7 || job.SourceComponent != "reading" {
		t.Fatalf("reading-only claim = %+v, %v", job, err)
	}
	if err := client.DeferSourceStage(context.Background(), job.ID, job.LeaseToken, "reading"); err != nil {
		t.Fatalf("local stage deferral: %v", err)
	}
}
