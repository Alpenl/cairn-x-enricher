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
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestListBookmarksCanSkipUnusedCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("counts") != "0" {
			t.Errorf("counts option = %q", request.URL.Query().Get("counts"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[{"id":7,"url":"https://x.com/a/status/7","status":"pending","processable":true,"related_links":[],"images":[]}],"next_before_id":null}`))
	}))
	defer server.Close()
	page, err := NewClient(server.URL, "token", server.Client()).ListBookmarks(context.Background(), BookmarkQuery{Limit: 20, SkipCounts: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != 7 {
		t.Fatalf("count-free page = %+v, %v", page, err)
	}
}

func TestClientClaimCompleteAndFail(t *testing.T) {
	t.Helper()
	var requests []map[string]any
	imageKey := "enrichment/7/" + strings.Repeat("a", 64) + ".jpg"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if strings.HasSuffix(request.URL.Path, "/claim") &&
			request.Header.Get("X-Cairn-Source-Lease-Admission") != "1" {
			t.Errorf("source claim omitted lease admission capability")
		}
		if strings.HasSuffix(request.URL.Path, "/claim") &&
			request.Header.Get("X-Cairn-Source-Component-Gate") != "1" {
			t.Errorf("source claim omitted component gate capability")
		}
		switch request.URL.Path {
		case "/api/enrichment/jobs/claim":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","note":"read","created_at":"2026-09-03T00:00:00Z","attempt":2,"lease_token":"lease-7","lease_until":"2026-09-03T00:15:00Z","content_revision":1}`))
		case "/api/enrichment/jobs/8/claim":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":8,"url":"https://x.com/a/status/2","note":"manual","created_at":"2026-09-03T00:00:00Z","attempt":1,"lease_token":"lease-8","lease_until":"2026-09-03T00:15:00Z","content_revision":1}`))
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
		case "/api/enrichment/jobs/7":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","status":"completed","images":[]}`))
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
			if request.URL.Query().Get("include_cache_identity") != "1" {
				t.Errorf("detail cache identity was not requested")
			}
			_, _ = writer.Write([]byte(`{"id":7,"url":"https://x.com/a/status/1","note":"手动备注","created_at":"2026-09-03T00:00:00Z","status":"completed","attempts":1,"ai_title":"人工智能生成的测试中文标题","original_language":"en","summary":"摘要","original_text":"完整原文","translated_text":"完整简体中文译文","related_links":[],"images":[],"model":"grok-4.6","cache_identity":{"schema_version":1,"content_revision":3,"body_revision":2,"personal_revision":0,"latest_decision_id":0,"latest_entity_revision":0}}`))
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
	if detail.OriginalText != "完整原文" || detail.TranslatedText != "完整简体中文译文" || detail.Status != "completed" ||
		detail.CacheIdentity == nil || detail.CacheIdentity.ContentRevision != 3 {
		t.Fatalf("GetBookmark() = %+v", detail)
	}
}

func TestClientSavesManualSourceAndChecksReceipt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/api/enrichment/jobs/7/manual-source" || request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("manual source request = %s %s, auth %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode source request: %v", err)
		}
		if body["operation_key"] != "op-7" || body["expected_revision"] != float64(3) || body["original_text"] != "full post" {
			t.Errorf("source request body = %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":7,"status":"source_saved","content_revision":4}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	for range 2 {
		receipt, err := client.SaveManualSource(context.Background(), 7, "op-7", 3, "full post")
		if err != nil || receipt.ContentRevision != 4 || receipt.Status != "source_saved" {
			t.Fatalf("SaveManualSource() = (%+v, %v)", receipt, err)
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want retry with same operation", requests)
	}
}

func TestClientEnqueuesDurableManualRequestAndPreservesBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["operation_key"] != "op-7" {
			t.Errorf("manual enqueue body = %#v, error=%v", body, err)
		}
		if request.URL.Path == "/api/enrichment/jobs/7/enqueue" {
			_, _ = writer.Write([]byte(`{"id":7,"status":"pending","action":"manual_process"}`))
			return
		}
		writer.Header().Set("Retry-After", "5")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"manual_queue_full"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	if err := client.RequestEnrichment(context.Background(), 7, "op-7"); err != nil {
		t.Fatalf("RequestEnrichment(7) = %v", err)
	}
	err := client.RequestEnrichment(context.Background(), 8, "op-7")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "manual_queue_full" ||
		apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RetryAfter != "5" {
		t.Fatalf("RequestEnrichment(8) = %v", err)
	}
}

func TestClientAdmitsPaidSourceStageOnlyWithWorkerReceipt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/api/enrichment/jobs/7/lease-admit" {
			t.Errorf("admission path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil ||
			body["lease_token"] != "lease-7" || body["stage"] != "reading" ||
			body["min_remaining_ms"] != float64(210000) {
			t.Errorf("admission body = %#v, error=%v", body, err)
		}
		if requests == 2 {
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(`{"error":"lease_released"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"id":7,"status":"admitted","remaining_ms":500000}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	if err := client.AdmitSourceStage(context.Background(), 7, "lease-7", "reading", 210*time.Second); err != nil {
		t.Fatalf("AdmitSourceStage() = %v", err)
	}
	err := client.AdmitSourceStage(context.Background(), 7, "lease-7", "reading", 210*time.Second)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "lease_released" || apiErr.Class() != enrich.ErrorClassStale {
		t.Fatalf("short lease = %v", err)
	}
}

func TestClientRequiresSourceLeaseContractBeforeScheduling(t *testing.T) {
	for _, valid := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/api/enrichment/source-lease-capability" ||
				request.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("capability request = %s, auth=%q", request.URL.Path, request.Header.Get("Authorization"))
			}
			if !valid {
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(`{"error":"not_found"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"protocol":1,"lease_ms":900000,"paid_stage_admission":true,"provider_result_guard":true,"completion_replay":true,"provider_attempt_ledger":true,"refresh_source_checkpoint":true,"source_component_gate":true}`))
		}))
		client := NewClient(server.URL, "token", server.Client())
		err := client.VerifySourceLeaseCapability(context.Background())
		server.Close()
		if valid && err != nil || !valid && err == nil {
			t.Fatalf("valid=%t, VerifySourceLeaseCapability()=%v", valid, err)
		}
	}
}

func TestSourceStageTransientReportsItsGateWithoutChangingContentFailures(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/enrichment/jobs/7/fail" {
			t.Errorf("failure path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode failure: %v", err)
		}
		bodies = append(bodies, body)
		_, _ = writer.Write([]byte(`{"id":7,"status":"failed"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	if err := client.FailSourceStage(context.Background(), 7, "lease-7", "fetch", "HTTP 529",
		12*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if err := client.FailSourceStage(context.Background(), 7, "lease-7", "reading", "bad stored content",
		0, false); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["component_fault"] != "source_transient" ||
		bodies[0]["retry_after_ms"] != float64(600000) || bodies[1]["component_fault"] != nil {
		t.Fatalf("failure bodies = %#v", bodies)
	}
}

func TestSourceClaimableRequiresAnAuthenticatedBoolean(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       bool
		wantError  bool
	}{
		{name: "empty", body: `{"claimable":false}`, want: false},
		{name: "pending", body: `{"claimable":true}`, want: true},
		{name: "missing field", body: `{}`, wantError: true},
		{name: "unknown field", body: `{"claimable":false,"other":1}`, wantError: true},
		{name: "old Worker", status: http.StatusNotFound, body: `{"error":"not_found"}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/api/enrichment/source-claimable" ||
					request.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("claimability request = %s %s, auth=%q", request.Method, request.URL.Path,
						request.Header.Get("Authorization"))
				}
				if test.status != 0 {
					writer.WriteHeader(test.status)
				}
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			got, err := NewClient(server.URL, "token", server.Client()).SourceClaimable(context.Background())
			if (err != nil) != test.wantError || err == nil && got != test.want {
				t.Fatalf("SourceClaimable() = %t, %v", got, err)
			}
		})
	}
}

func TestClientRejectsWorkerWithoutAtomicRefreshCheckpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"protocol":1,"lease_ms":900000,"paid_stage_admission":true,"provider_result_guard":true,"completion_replay":true,"provider_attempt_ledger":true}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	if err := client.VerifySourceLeaseCapability(context.Background()); err == nil {
		t.Fatal("old Worker without atomic refresh checkpoint was accepted")
	}
}

func TestClientRefreshSourceForwardsStableOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/enrichment/jobs/7/refresh-source" {
			t.Errorf("refresh path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["operation_key"] != "refresh-7" {
			t.Errorf("refresh body = %#v, error=%v", body, err)
		}
		_, _ = writer.Write([]byte(`{"id":7,"status":"pending","action":"refresh_source","content_revision":3,"preserves":[]}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	for range 2 {
		payload, err := client.RefreshSourceWithOperation(context.Background(), 7, "refresh-7")
		if err != nil || !strings.Contains(string(payload), `"content_revision":3`) {
			t.Fatalf("RefreshSourceWithOperation() = (%s, %v)", payload, err)
		}
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
	response, err := NewClient(server.URL, "token", server.Client()).GetImage(context.Background(), "../secret")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
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
