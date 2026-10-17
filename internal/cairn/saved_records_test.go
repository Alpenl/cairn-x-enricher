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

func TestSavedRecordsBoundScopeAndPreserveOriginalData(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.Header.Get("X-Cairn-App-Saved-Records") != "1" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("lost explicit authenticated read")
		}
		w.Header().Set("X-Cairn-App-Saved-Records", "1")
		if r.URL.Path != "/api/v2/links/4/saved-records/entities" || r.URL.Query().Get("source_revision") != "2" {
			t.Error("lost source scope")
		}
		if key := r.URL.Query().Get("record_key"); key != "" {
			if key != "结果 / + ?" {
				t.Error("opaque operation key changed")
			}
			_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"kind":"entities","source_revision":2,"record":{"key":"结果 / + ?","state":"failed","outcome":"ignored_stale","payload":{"unknown":null,"entities":[]}}}`)
		} else {
			_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"kind":"entities","source_revision":2,"records":[{"key":"𐀀"},{"key":""}],"next_after":null}`)
		}
	}))
	defer server.Close()
	c := NewClient(server.URL, "fixture", server.Client())
	for _, key := range []string{"", "结果 / + ?"} {
		data, err := c.GetSavedRecords(context.Background(), 4, SavedRecordQuery{Kind: "entities", SourceRevision: 2, RecordKey: key})
		if err != nil || !json.Valid(data) {
			t.Fatalf("stored read failed: %s %v", data, err)
		}
		if key != "" && (!strings.Contains(string(data), `"unknown":null`) || strings.Contains(string(data), "model")) {
			t.Fatal("historical metadata fabricated or lost")
		}
	}
	if requests != 2 {
		t.Fatal("unexpected fallback")
	}
}

func TestSavedRecordsRejectMalformedForeignOversizedAndUnsortedPages(t *testing.T) {
	for _, body := range []string{
		`{"records":null,"next_after":null}`, `{"records":[],"next_after":"1"}`, `{"records":[]}`,
		`{"records":[{"key":"2"},{"key":"3"}],"next_after":null}`,
		`{"records":[{"key":"01"}],"next_after":null}`,
		`{"records":[{"key":"1","payload":{}}],"next_after":null}`,
		`{"records":[],"next_after":null,"large":"` + strings.Repeat("x", 128<<10) + `"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Cairn-App-Saved-Records", "1")
			_, _ = fmt.Fprint(w, `{"version":1,"bookmark_id":4,"kind":"sources","source_revision":null,`+strings.TrimPrefix(body, "{"))
		}))
		_, err := NewClient(server.URL, "fixture", server.Client()).GetSavedRecords(context.Background(), 4, SavedRecordQuery{Kind: "sources"})
		server.Close()
		if err == nil {
			t.Fatal("invalid history accepted")
		}
	}
	c := NewClient("http://127.0.0.1:1", "fixture", nil)
	for _, q := range []SavedRecordQuery{{Kind: "unknown"}, {Kind: "entities"}, {Kind: "sources", SourceRevision: 1},
		{Kind: "events", RecordKey: "1", AfterKey: "2"}, {Kind: "sources", AfterKey: "01"},
		{Kind: "entities", SourceRevision: 1, RecordKey: strings.Repeat("𐀀", 101)}} {
		if _, err := c.GetSavedRecords(context.Background(), 4, q); err == nil || strings.Contains(err.Error(), "connect") {
			t.Fatal("invalid reference reached network")
		}
	}
}

func TestSavedRecordsUnsupportedIsNotUnavailableOrEmpty(t *testing.T) {
	for _, test := range []struct {
		status int
		ack    string
		want   error
	}{{404, "", ErrV2Unsupported}, {501, "", ErrV2Unsupported}, {200, "", ErrV2Unsupported},
		{404, "1", ErrRunHistoryUnavailable}, {410, "1", ErrRunHistoryUnavailable}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Cairn-App-Saved-Records", test.ack)
			w.WriteHeader(test.status)
			_, _ = fmt.Fprint(w, `{}`)
		}))
		_, err := NewClient(server.URL, "fixture", server.Client()).GetSavedRecords(context.Background(), 4, SavedRecordQuery{Kind: "events"})
		server.Close()
		if !errors.Is(err, test.want) {
			t.Fatalf("wrong history state: %v", err)
		}
	}
}

func TestHistoryReadsDoNotForwardCredentialsToRedirectTargets(t *testing.T) {
	followed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/must-not-follow" {
			followed++
			_, _ = fmt.Fprint(w, `{}`)
			return
		}
		w.Header().Set("Location", "/must-not-follow")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	transport := server.Client()
	c := NewClient(server.URL, "synthetic-history", transport)
	if _, err := c.GetSavedRecords(context.Background(), 4, SavedRecordQuery{Kind: "sources"}); err == nil {
		t.Fatal("redirect accepted as history")
	}
	if _, err := c.GetAppRunHistory(context.Background(), 4, 0, 0); err == nil {
		t.Fatal("redirect accepted as run history")
	}
	if followed != 0 || transport.CheckRedirect != nil {
		t.Fatalf("history followed redirect or mutated shared client: followed=%d", followed)
	}
}
