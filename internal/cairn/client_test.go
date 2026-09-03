package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestClientClaimCompleteAndFail(t *testing.T) {
	t.Helper()
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/enrichment/jobs/claim":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","note":"read","created_at":"2026-09-03T00:00:00Z","attempt":2,"lease_token":"lease-7","lease_until":"2026-09-03T00:15:00Z"}`))
		case "/api/enrichment/jobs/8/claim":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":8,"url":"https://x.com/a/status/2","note":"manual","created_at":"2026-09-03T00:00:00Z","attempt":1,"lease_token":"lease-8","lease_until":"2026-09-03T00:15:00Z"}`))
		case "/api/enrichment/jobs/7/complete", "/api/enrichment/jobs/7/fail":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			body["path"] = request.URL.Path
			requests = append(requests, body)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token", server.Client())
	job, err := client.Claim(context.Background())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.ID != 7 || job.Attempt != 2 || job.LeaseToken != "lease-7" {
		t.Fatalf("Claim() = %+v", job)
	}
	manualJob, err := client.ClaimByID(context.Background(), 8)
	if err != nil {
		t.Fatalf("ClaimByID() error = %v", err)
	}
	if manualJob.ID != 8 || manualJob.LeaseToken != "lease-8" {
		t.Fatalf("ClaimByID() = %+v", manualJob)
	}

	completion := Completion{
		LeaseToken:   job.LeaseToken,
		OriginalText: "source",
		Summary:      "summary",
		RelatedLinks: []string{"https://example.com/article"},
		Model:        "grok-4.6",
	}
	if err := client.Complete(context.Background(), job.ID, completion); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := client.Fail(context.Background(), job.ID, job.LeaseToken, "temporary"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0]["lease_token"] != "lease-7" || requests[0]["model"] != "grok-4.6" {
		t.Fatalf("completion request = %#v", requests[0])
	}
	if requests[1]["error"] != "temporary" {
		t.Fatalf("failure request = %#v", requests[1])
	}
}

func TestClientListsAndGetsBookmarks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/enrichment/jobs":
			if got := request.URL.Query().Get("limit"); got != "20" {
				t.Errorf("limit = %q", got)
			}
			if got := request.URL.Query().Get("before_id"); got != "90" {
				t.Errorf("before_id = %q", got)
			}
			if got := request.URL.Query().Get("status"); got != "completed" {
				t.Errorf("status = %q", got)
			}
			if got := request.URL.Query().Get("q"); got != "代理 & Go" {
				t.Errorf("q = %q", got)
			}
			_, _ = writer.Write([]byte(`{"items":[{"id":7,"url":"https://x.com/a/status/1","note":"收藏","created_at":"2026-09-03T00:00:00Z","status":"completed","attempts":1,"summary":"摘要","related_links":["https://example.com/source"],"model":"grok-4.6","enriched_at":"2026-09-03T00:01:00Z"}],"next_before_id":7,"counts":{"total":2,"pending":1,"processing":0,"completed":1,"failed":0,"exhausted":0}}`))
		case "/api/enrichment/jobs/7":
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","note":"收藏","created_at":"2026-09-03T00:00:00Z","status":"completed","attempts":1,"summary":"摘要","original_text":"完整原文","related_links":[],"model":"grok-4.6"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", server.Client())
	page, err := client.ListBookmarks(context.Background(), BookmarkQuery{
		Limit: 20, BeforeID: 90, Status: "completed", Search: "代理 & Go",
	})
	if err != nil {
		t.Fatalf("ListBookmarks() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Summary != "摘要" || page.Counts.Total != 2 {
		t.Fatalf("ListBookmarks() = %+v", page)
	}
	if page.NextBeforeID == nil || *page.NextBeforeID != 7 {
		t.Fatalf("NextBeforeID = %v", page.NextBeforeID)
	}

	detail, err := client.GetBookmark(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetBookmark() error = %v", err)
	}
	if detail.OriginalText != "完整原文" || detail.Status != "completed" {
		t.Fatalf("GetBookmark() = %+v", detail)
	}
}

func TestClientClaimReturnsNilForEmptyQueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	job, err := NewClient(server.URL, "token", server.Client()).Claim(context.Background())
	if err != nil || job != nil {
		t.Fatalf("Claim() = (%+v, %v), want (nil, nil)", job, err)
	}
}

func TestClientReturnsStableAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"error":"lease_conflict"}`))
	}))
	defer server.Close()

	err := NewClient(server.URL, "token", server.Client()).Complete(context.Background(), 1, Completion{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Complete() error = %T %v", err, err)
	}
	if !reflect.DeepEqual(apiErr, &APIError{StatusCode: http.StatusConflict, Code: "lease_conflict"}) {
		t.Fatalf("APIError = %+v", apiErr)
	}
}

func TestClientRejectsMalformedClaim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":1,"unexpected":true}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "token", server.Client()).Claim(context.Background()); err == nil {
		t.Fatal("Claim() error = nil, want malformed response error")
	}
}
