package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestSelectionProxyPreservesOptionalStoredLifecycle(t *testing.T) {
	for _, stored := range []string{"", `{"version":1,"personal_revision":7,"entities":{"status":"not_run","automatic":[],"values":[{"term":"Manual","origin":"human","confirmed":true,"revision":7}]}}`} {
		t.Run(map[bool]string{true: "legacy-unknown", false: "stored-human-name"}[stored == ""], func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/links/4/selection" || r.URL.Query().Get("include_state") != "1" ||
					r.URL.Query().Get("include_automatic") != "1" || r.Header.Get("Authorization") != "Bearer test-internal" {
					t.Errorf("selection identity/capability changed: %s", r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				payload := map[string]any{"revision": 7, "selection": map[string]any{}}
				if stored != "" {
					payload["state"] = json.RawMessage(stored)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer worker.Close()
			backend := cairn.NewClient(worker.URL, "test-internal", worker.Client())
			server := &Server{backend: backend, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/4/v2-selection", nil)
			request.SetPathValue("id", "4")
			response := httptest.NewRecorder()
			server.getV2Selection(response, request)
			var result map[string]json.RawMessage
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
				t.Fatalf("selection unavailable: %d %s", response.Code, response.Body.String())
			}
			if stored == "" {
				if _, present := result["state"]; present {
					t.Fatal("legacy absence was presented as a known empty lifecycle")
				}
			} else if strings.TrimSpace(string(result["state"])) != stored {
				t.Fatalf("stored state was rewritten: %s", result["state"])
			}
		})
	}
}

func TestSelectionAndEntityWriteProxyDistinguishUnsupportedWorker(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"unsupported"}`))
			}))
			defer worker.Close()
			server := &Server{backend: cairn.NewClient(worker.URL, "test-internal", worker.Client()),
				logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				request := httptest.NewRequestWithContext(context.Background(), method, "/",
					strings.NewReader(`{"field":"entities","action":"accept","term":"Manual","operation_key":"unsupported","expected_revision":0}`))
				request.SetPathValue("id", "4")
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				if method == http.MethodGet {
					server.getV2Selection(response, request)
					var result map[string]any
					if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
						result["available"] != false || result["reason"] != "v2_unsupported" {
						t.Fatalf("unsupported read became known data/error: %d %s", response.Code, response.Body.String())
					}
				} else {
					server.applyV2Override(response, request)
					if response.Code != 409 || !strings.Contains(response.Body.String(), "v2_unsupported") {
						t.Fatalf("unsupported write lost its capability result: %d %s", response.Code, response.Body.String())
					}
				}
			}
		})
	}
}
