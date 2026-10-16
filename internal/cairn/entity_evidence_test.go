package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEntityEvidenceReferenceRejectsAmbiguousAndUnboundedValues(t *testing.T) {
	query := url.Values{"snapshot_id": {"12"}, "content_revision": {"3"}, "block_id": {"片段🙂"}, "start": {"2"}, "end": {"6"}}
	ref, err := ParseEntityEvidenceReference(query)
	if err != nil || ref.BlockID != "片段🙂" || ref.SnapshotID != 12 || ref.Start != 2 || ref.End != 6 {
		t.Fatalf("valid reference: %+v %v", ref, err)
	}
	for key, value := range map[string]string{"snapshot_id": "9007199254740992", "content_revision": "01", "start": "-1", "end": "100001", "block_id": strings.Repeat("🙂", 33)} {
		changed, _ := url.ParseQuery(query.Encode())
		changed.Set(key, value)
		if _, err := ParseEntityEvidenceReference(changed); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
	}
	query.Add("start", "2")
	if _, err := ParseEntityEvidenceReference(query); err == nil {
		t.Fatal("duplicate query value accepted")
	}
}

func TestEntityEvidenceClientChecksCapabilityBindingsAndBodyLimit(t *testing.T) {
	ref := EntityEvidenceReference{SnapshotID: 12, ContentRevision: 3, BlockID: "片段🙂&'", Start: 2, End: 6}
	for _, test := range []struct {
		name   string
		status int
		ack    string
		body   string
		want   error
		ok     bool
	}{
		{"valid", 200, "1", "", nil, true},
		{"old route", 404, "", "", ErrV2Unsupported, false},
		{"missing stored material", 404, "1", "", ErrEntityEvidenceUnavailable, false},
		{"reference changed", 409, "1", "", ErrEntityEvidenceChanged, false},
		{"unacknowledged success", 200, "", "", ErrV2Unsupported, false},
		{"unknown payload version", 200, "1", "{\"version\":2}", ErrV2Unsupported, false},
		{"authorization", 401, "", `{"error":"invalid_token"}`, nil, false},
		{"wrong bookmark", 200, "1", `{"version":1,"bookmark_id":999}`, nil, false},
		{"oversized", 200, "1", strings.Repeat("private-source-canary", 2000), nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/links/7/entity-evidence" || r.Header.Get("Authorization") != "Bearer fixture-internal" || r.ContentLength != 0 {
					t.Error("read escaped its exact authenticated endpoint")
				}
				actual, err := ParseEntityEvidenceReference(r.URL.Query())
				if err != nil || actual != ref {
					t.Errorf("query changed in transit: %+v %v", actual, err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cairn-Entity-Evidence", test.ack)
				w.WriteHeader(test.status)
				if test.body != "" {
					_, _ = w.Write([]byte(test.body))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "bookmark_id": 7,
					"evidence_snapshot_id": 12, "content_revision": 3, "block_id": ref.BlockID,
					"span_start": 2, "span_end": 6, "before": "前🙂", "quote": "Acme", "after": " 后𠮷"})
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture-internal", server.Client())
			payload, err := client.GetEntityEvidence(context.Background(), 7, ref)
			if test.ok {
				if err != nil || !strings.Contains(string(payload), "Acme") {
					t.Fatalf("valid source lost: %s %v", payload, err)
				}
			} else if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("expected failure %v; got %v", test.want, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-source-canary") {
				t.Fatal("source body leaked into a loggable error")
			}
		})
	}
}
