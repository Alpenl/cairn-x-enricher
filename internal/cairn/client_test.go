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
