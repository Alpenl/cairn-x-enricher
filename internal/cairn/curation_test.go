package cairn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validBookmarkJSON(id string, extra string) string {
	return `{"id":` + id + `,"url":"https://x.com/a/status/` + id + `","note":"","created_at":"2026-09-03T00:00:00Z",` +
		`"status":"completed","curation_status":"inbox","classification_reviewed":false,` +
		`"related_links":[],"images":[]` + extra + `}`
}

func TestGetTaxonomyValidatesTheCatalog(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"version":"2026-09-08.1","topics":[{"id":"llm","label":"LLM","aliases":[],"active":true}],"forms":[{"id":"tool","label":"工具","aliases":[],"active":true}],"uses":[{"id":"try","label":"待试","aliases":[],"active":true}]}`))
	}))
	defer server.Close()

	catalog, err := NewClient(server.URL, "token", server.Client()).GetTaxonomy(context.Background())
	if err != nil {
		t.Fatalf("GetTaxonomy() error = %v", err)
	}
	if gotPath != "/api/enrichment/taxonomy" {
		t.Fatalf("path = %q", gotPath)
	}
	if catalog.Version != "2026-09-08.1" || len(catalog.Topics) != 1 {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestGetTaxonomyRejectsAnInvalidCatalog(t *testing.T) {
	// An invalid vocabulary must fail at load time rather than reaching the
	// model prompt and the closed schema.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"version":"","topics":[]}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "token", server.Client()).GetTaxonomy(context.Background()); err == nil {
		t.Fatal("GetTaxonomy() error = nil, want catalog validation failure")
	}
}

func TestGetTaxonomyReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "token", server.Client()).GetTaxonomy(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GetTaxonomy() error = %T %v", err, err)
	}
}

func TestUpdateCurationSendsPatchAndValidatesResponse(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method, path = request.Method, request.URL.Path
		payload := make([]byte, request.ContentLength)
		_, _ = request.Body.Read(payload)
		body = string(payload)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(validBookmarkJSON("11", `,"why":"项目评审","curation_status":"kept"`)))
	}))
	defer server.Close()

	why := "项目评审"
	detail, err := NewClient(server.URL, "token", server.Client()).UpdateCuration(context.Background(), 11, CurationUpdate{Why: &why})
	if err != nil {
		t.Fatalf("UpdateCuration() error = %v", err)
	}
	if method != http.MethodPatch || path != "/api/enrichment/jobs/11/curation" {
		t.Fatalf("request = %s %s", method, path)
	}
	if !strings.Contains(body, `"why":"项目评审"`) {
		t.Fatalf("request body = %q", body)
	}
	if detail.ID != 11 || detail.CurationStatus != "kept" {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestUpdateCurationRejectsInvalidID(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "token", http.DefaultClient)
	if _, err := client.UpdateCuration(context.Background(), 0, CurationUpdate{}); err == nil {
		t.Fatal("UpdateCuration() accepted a non-positive ID")
	}
}

func TestUpdateCurationRejectsMismatchedResponse(t *testing.T) {
	// A response for a different bookmark must not be applied to this one.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(validBookmarkJSON("12", "")))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "token", server.Client()).UpdateCuration(context.Background(), 11, CurationUpdate{}); err == nil {
		t.Fatal("UpdateCuration() accepted a response for another bookmark")
	}
}

func TestAPIErrorStringIncludesTheCode(t *testing.T) {
	if got := (&APIError{StatusCode: 409, Code: "job_busy"}).Error(); !strings.Contains(got, "job_busy") || !strings.Contains(got, "409") {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&APIError{StatusCode: 500}).Error(); got != "cairn API returned HTTP 500" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestGetImageRejectsMalformedKeys(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "token", http.DefaultClient)
	for _, key := range []string{
		"", "enrichment/0/" + strings.Repeat("a", 64) + ".jpg",
		"enrichment/1/" + strings.Repeat("a", 63) + ".jpg",
		"enrichment/1/" + strings.Repeat("a", 64) + ".exe",
		"../etc/passwd",
	} {
		response, err := client.GetImage(context.Background(), key)
		if err == nil {
			_ = response.Body.Close()
			t.Errorf("GetImage(%q) error = nil, want rejection", key)
		}
	}
}
