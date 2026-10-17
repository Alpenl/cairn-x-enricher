package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppRunHistoryPreservesStoredNullsAndLoadsOnlyChosenPayload(t *testing.T) {
	const stored = `{"id":9,"resolved_model":null,"usage":{},"raw_judgments":{"source":{"partial":true}},"answers":{"topic":false},"status":"failed"}`
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Method != http.MethodGet || r.Header.Get("X-Cairn-App-Run-History") != "1" {
			t.Error("not an explicit read")
		}
		w.Header().Set("X-Cairn-App-Run-History", "1")
		if strings.HasSuffix(r.URL.Path, "/9") {
			_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"run":`+stored+`}`)
		} else {
			_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"runs":[{"id":9,"resolved_model":null,"status":"failed"}],"next_after_id":9}`)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	page, err := client.GetAppRunHistory(context.Background(), 4, 0, 14)
	if err != nil || !strings.Contains(string(page), `"resolved_model":null`) || len(paths) != 1 || paths[0] != "/api/v2/links/4/run-history?after_id=14" {
		t.Fatalf("bounded page changed stored metadata: %s paths=%v err=%v", page, paths, err)
	}
	detail, err := client.GetAppRunHistory(context.Background(), 4, 9, 0)
	var decoded struct {
		Version    int             `json:"version"`
		BookmarkID int64           `json:"bookmark_id"`
		Run        json.RawMessage `json:"run"`
	}
	if err != nil || json.Unmarshal(detail, &decoded) != nil || decoded.Version != 1 || decoded.BookmarkID != 4 || string(decoded.Run) != stored || len(paths) != 2 || paths[1] != "/api/v2/links/4/run-history/9" {
		t.Fatalf("explicit detail did not retain stored wire record: %s err=%v paths=%v", detail, err, paths)
	}
}

func TestAppRunHistoryRejectsWrongIdentityCursorUnboundedAndLegacyPayloads(t *testing.T) {
	for _, test := range []struct {
		name, body string
		run, after int64
	}{
		{"wrong-detail", `{"id":10}`, 9, 0},
		{"null-detail", `null`, 9, 0},
		{"bad-json", `{`, 0, 0},
		{"null-list", `{"runs":null,"next_after_id":null}`, 0, 0},
		{"ascending", `{"runs":[{"id":2},{"id":3}],"next_after_id":null}`, 0, 0},
		{"repeated", `{"runs":[{"id":3}],"next_after_id":3}`, 0, 3},
		{"bad-next", `{"runs":[{"id":3}],"next_after_id":4}`, 0, 0},
		{"empty-next", `{"runs":[],"next_after_id":4}`, 0, 0},
		{"missing-next", `{"runs":[]}`, 0, 0},
		{"unsafe-id", `{"runs":[{"id":9007199254740992}],"next_after_id":null}`, 0, 0},
		{"legacy-full-list", `{"runs":[{"id":3,"answers":{}}],"next_after_id":null}`, 0, 0},
		{"oversize-list", `{"ignored":"` + strings.Repeat("x", 128<<10) + `","runs":[],"next_after_id":null}`, 0, 0},
		{"oversize-run", `{"id":9,"ignored":"` + strings.Repeat("x", 5<<20) + `"}`, 9, 0},
		{"invalid-utf8", "{\"id\":9,\"model\":\"\xff\"}", 9, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("X-Cairn-App-Run-History", "1")
				if test.run > 0 {
					_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"run":`+test.body+`}`)
				} else {
					_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,`+strings.TrimPrefix(test.body, "{"))
				}
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "fixture", server.Client()).GetAppRunHistory(context.Background(), 4, test.run, test.after)
			if err == nil || calls != 1 {
				t.Fatalf("invalid record accepted or fallback attempted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAppRunHistoryDistinguishesMissingCapabilityFromUnavailableRecord(t *testing.T) {
	for _, test := range []struct {
		status int
		ack    string
		want   error
	}{
		{404, "", ErrV2Unsupported}, {405, "", ErrV2Unsupported}, {501, "", ErrV2Unsupported},
		{200, "", ErrV2Unsupported}, {404, "1", ErrRunHistoryUnavailable}, {410, "1", ErrRunHistoryUnavailable},
	} {
		t.Run(fmt.Sprint(test.status, test.ack), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Cairn-App-Run-History", test.ack)
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture", server.Client())
			_, err := client.GetAppRunHistory(context.Background(), 4, 9, 0)
			if !errors.Is(err, test.want) {
				t.Fatalf("wrong capability state: %v", err)
			}
		})
	}
	client := NewClient("http://127.0.0.1:1", "fixture", nil)
	for _, ids := range [][3]int64{{0, 0, 0}, {4, -1, 0}, {4, 9, 2}, {1 << 53, 0, 0}, {4, 0, -1}} {
		if _, err := client.GetAppRunHistory(context.Background(), ids[0], ids[1], ids[2]); err == nil || strings.Contains(err.Error(), "connect") {
			t.Fatalf("invalid reference reached network: %v %v", ids, err)
		}
	}
}
