package cairn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunHistoryReadsBoundedSummariesAndOnlyOneArchivedPayload(t *testing.T) {
	var summaries, details int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Cairn-Run-History") != "1" {
			t.Error("missing history capability")
		}
		w.Header().Set("X-Cairn-Run-History", "1")
		switch r.URL.Path {
		case "/api/v2/links/4/runs":
			summaries++
			if r.URL.Query().Get("view") != "summary" || r.URL.Query().Get("limit") != "50" {
				t.Error("loaded unbounded full history")
			}
			if summaries == 1 {
				_ = json.NewEncoder(w).Encode(RunSummaryPage{Runs: []RunSummary{{ID: 5, Status: "failed", Coverage: "partial"}}, NextAfterID: int64Pointer(5)})
			} else {
				if r.URL.Query().Get("after_id") != "5" {
					t.Error("history cursor not forwarded")
				}
				_ = json.NewEncoder(w).Encode(RunSummaryPage{Runs: []RunSummary{{ID: 3, Status: "succeeded", Coverage: "complete", Archived: true}}})
			}
		case "/api/v2/links/4/runs/3":
			details++
			_ = json.NewEncoder(w).Encode(StoredRun{ID: 3, Status: "succeeded", Coverage: "complete", Answers: json.RawMessage(`{}`), Archived: true})
		default:
			t.Errorf("unexpected history read %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	run, err := NewClient(server.URL, "fixture", server.Client()).GetReplayableRun(context.Background(), 4)
	if err != nil || run == nil || run.ID != 3 || !run.Archived || summaries != 2 || details != 1 {
		t.Fatalf("history hydrated unnecessary payload: summaries=%d details=%d run=%+v err=%v", summaries, details, run, err)
	}
}
func int64Pointer(v int64) *int64 { return &v }

func TestRunHistoryLegacyFallbackAndCursorSafety(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if modern {
					w.Header().Set("X-Cairn-Run-History", "1")
					_, _ = fmt.Fprint(w, `{"runs":[{"id":5,"status":"failed"}],"next_after_id":6}`)
				} else {
					_, _ = fmt.Fprint(w, `{"runs":[{"id":3,"status":"succeeded","coverage":"complete","answers":{}}]}`)
				}
			}))
			defer server.Close()
			run, err := NewClient(server.URL, "fixture", server.Client()).GetReplayableRun(context.Background(), 4)
			if modern && (err == nil || calls != 1) {
				t.Fatal("invalid next cursor was followed")
			}
			if !modern && (err != nil || run == nil || run.ID != 3 || calls != 2) {
				t.Fatalf("legacy fallback: run=%v calls=%d err=%v", run, calls, err)
			}
		})
	}
}
