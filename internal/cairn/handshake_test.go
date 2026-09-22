package cairn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestHandshakeDeclaresCapabilitiesAndReportsSupport(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/enrichment/classifications/target" {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodGet {
			t.Errorf("handshake method = %s, want GET", request.Method)
		}
		gotQuery = request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"target":{"generation":3,"spec_id":"classify-v1","spec_hash":"sha256:x","taxonomy_version":"2026-09-20.1","policy_version":"jev-tags-v1","requested_model":"jev-latest","protocol":"v2"},"supported":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", server.Client())
	result, err := client.Handshake(context.Background(), ClassificationCapabilities("classify-v1", "2026-09-20.1", "jev-latest"))
	if err != nil {
		t.Fatalf("Handshake() error = %v", err)
	}
	if !result.Supported || result.Target.Generation != 3 || result.Target.SpecID != "classify-v1" {
		t.Fatalf("Handshake() = %+v", result)
	}
	for _, want := range []string{"protocol=v2", "spec_ids=classify-v1", "policy_versions=jev-policy-v3", "models=jev-latest", "taxonomy_versions=2026-09-20.1"} {
		if !contains(gotQuery, want) {
			t.Fatalf("handshake query %q missing %q", gotQuery, want)
		}
	}
}

func TestClaimClassificationPausesOnCapabilityMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/enrichment/classifications/target":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"target":{"generation":1,"spec_id":"classify-v2","spec_hash":"sha256:y","taxonomy_version":"2026-09-20.1","policy_version":"jev-tags-v2","requested_model":"jev-pinned","protocol":"v2"},"supported":false}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", server.Client())
	_, err := client.ClaimClassification(context.Background(), "classify-v1", "2026-09-20.1", "jev-latest")
	if err == nil {
		t.Fatal("ClaimClassification() expected an unsupported-target error")
	}
	if !enrich.PausesComponent(err) {
		t.Fatalf("capability mismatch class = %s, want component pause", enrich.ClassOf(err))
	}
}

func TestAPIErrorClassMapping(t *testing.T) {
	cases := []struct {
		code  string
		class enrich.ErrorClass
	}{
		{"capability_mismatch", enrich.ErrorClassConfiguration},
		{"configuration_error", enrich.ErrorClassConfiguration},
		{"target_changed", enrich.ErrorClassStale},
		{"input_changed", enrich.ErrorClassStale},
		{"lease_expired", enrich.ErrorClassStale},
		{"already_completed", enrich.ErrorClassCompleted},
		{"operation_conflict", enrich.ErrorClassContract},
		{"invalid_source", enrich.ErrorClassContract},
	}
	for _, testCase := range cases {
		err := &APIError{StatusCode: http.StatusConflict, Code: testCase.code}
		if got := enrich.ClassOf(err); got != testCase.class {
			t.Errorf("APIError{%s}.Class() = %s, want %s", testCase.code, got, testCase.class)
		}
	}
	// A bare 409 without a typed code must not be silently treated as success.
	if got := enrich.ClassOf(&APIError{StatusCode: http.StatusConflict}); got != enrich.ErrorClassStale {
		t.Errorf("bare 409 class = %s, want stale", got)
	}
	// 401 is a configuration problem, not a transient retry.
	if got := enrich.ClassOf(&APIError{StatusCode: http.StatusUnauthorized}); got != enrich.ErrorClassConfiguration {
		t.Errorf("401 class = %s, want configuration", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
