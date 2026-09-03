package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestClientClaimCompleteAndFail(t *testing.T) {
	t.Helper()
	var requests []map[string]any
	imageKey := "enrichment/7/" + strings.Repeat("a", 64) + ".jpg"
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
		case "/api/enrichment/jobs/7/images":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			body["path"] = request.URL.Path
			requests = append(requests, body)
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"images": []ImageRef{{Key: imageKey, ContentType: "image/jpeg"}},
			})
		case "/api/enrichment/images/" + imageKey:
			writer.Header().Set("Content-Type", "image/jpeg")
			writer.Header().Set("ETag", `"test-image"`)
			_, _ = writer.Write([]byte("jpeg-data"))
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
		LeaseToken:       job.LeaseToken,
		AITitle:          "人工智能生成的测试中文标题",
		OriginalLanguage: "en",
		OriginalText:     "source",
		TranslatedText:   "中文译文",
		Summary:          "summary",
		RelatedLinks:     []string{"https://example.com/article"},
		Images:           []ImageRef{{Key: imageKey, ContentType: "image/jpeg"}},
		Model:            "grok-4.6",
	}
	images, err := client.StoreImages(context.Background(), job.ID, job.LeaseToken, []string{
		"https://pbs.twimg.com/media/abc?format=jpg&name=large",
	})
	if err != nil || len(images) != 1 || images[0].Key != imageKey {
		t.Fatalf("StoreImages() = (%+v, %v)", images, err)
	}
	if err := client.Complete(context.Background(), job.ID, completion); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := client.Fail(context.Background(), job.ID, job.LeaseToken, "temporary"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}

	imageResponse, err := client.GetImage(context.Background(), imageKey)
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	imageBody, err := io.ReadAll(imageResponse.Body)
	_ = imageResponse.Body.Close()
	if err != nil || string(imageBody) != "jpeg-data" || imageResponse.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("GetImage() body = %q, content type = %q, error = %v", imageBody, imageResponse.Header.Get("Content-Type"), err)
	}

	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0]["lease_token"] != "lease-7" || requests[0]["path"] != "/api/enrichment/jobs/7/images" {
		t.Fatalf("image request = %#v", requests[0])
	}
	if requests[1]["lease_token"] != "lease-7" || requests[1]["model"] != "grok-4.6" || requests[1]["ai_title"] == "" {
		t.Fatalf("completion request = %#v", requests[1])
	}
	if requests[2]["error"] != "temporary" {
		t.Fatalf("failure request = %#v", requests[2])
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
			_, _ = writer.Write([]byte(`{"items":[{"id":7,"url":"https://x.com/a/status/1","note":"手动备注","created_at":"2026-09-03T00:00:00Z","status":"completed","processable":true,"attempts":1,"ai_title":"人工智能生成的测试中文标题","original_language":"en","original_text":"完整原文","translated_text":"完整简体中文译文","summary":"摘要","related_links":["https://example.com/source"],"images":[{"key":"enrichment/7/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg","content_type":"image/jpeg"}],"model":"grok-4.6","enriched_at":"2026-09-03T00:01:00Z"}],"next_before_id":7,"counts":{"total":3,"pending":1,"processing":0,"completed":1,"failed":0,"exhausted":0,"unsupported":1}}`))
		case "/api/enrichment/jobs/7":
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","note":"手动备注","created_at":"2026-09-03T00:00:00Z","status":"completed","attempts":1,"ai_title":"人工智能生成的测试中文标题","original_language":"en","summary":"摘要","original_text":"完整原文","translated_text":"完整简体中文译文","related_links":[],"images":[],"model":"grok-4.6"}`))
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
	if len(page.Items) != 1 || page.Items[0].AITitle != "人工智能生成的测试中文标题" || page.Items[0].Note != "手动备注" || page.Items[0].TranslatedText != "完整简体中文译文" || len(page.Items[0].Images) != 1 || !page.Items[0].Processable || page.Counts.Unsupported != 1 {
		t.Fatalf("ListBookmarks() = %+v", page)
	}
	if page.NextBeforeID == nil || *page.NextBeforeID != 7 {
		t.Fatalf("NextBeforeID = %v", page.NextBeforeID)
	}

	detail, err := client.GetBookmark(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetBookmark() error = %v", err)
	}
	if detail.OriginalText != "完整原文" || detail.TranslatedText != "完整简体中文译文" || detail.Status != "completed" {
		t.Fatalf("GetBookmark() = %+v", detail)
	}
}

func TestClientRejectsUnsafeImageReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[{"id":7,"url":"https://x.com/a/status/1","status":"completed","attempts":1,"related_links":[],"images":[{"key":"enrichment/8/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg","content_type":"image/jpeg"}]}],"next_before_id":null,"counts":{}}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "token", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{}); err == nil {
		t.Fatal("ListBookmarks() error = nil, want invalid image reference error")
	}
	if _, err := NewClient(server.URL, "token", server.Client()).GetImage(context.Background(), "../secret"); err == nil {
		t.Fatal("GetImage() error = nil, want invalid key error")
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
