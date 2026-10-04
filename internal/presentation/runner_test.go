package presentation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunnerKeepsSourceAndUsesSeparateFormattingContract(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "model_failure"}[fail], func(t *testing.T) {
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				switch r.URL.Path {
				case "/api/enrichment/presentations/claim":
					if r.Header.Get("Authorization") != "Bearer worker" {
						t.Error("worker auth")
					}
					_, _ = w.Write([]byte(`{"link_id":1,"input_text":"保留 123。","lease_token":"lease"}`))
				case "/responses":
					if r.Header.Get("Authorization") != "Bearer formatter" {
						t.Error("model auth")
					}
					if _, ok := body["tools"]; ok {
						t.Error("formatter must not search")
					}
					if body["input"].([]any)[1].(map[string]any)["content"] != "保留 123。" {
						t.Error("source changed")
					}
					if fail {
						w.WriteHeader(503)
						return
					}
					_, _ = w.Write([]byte(`{"status":"completed","output":[{"content":[{"type":"output_text","text":"{\"formatted_content\":\"## 保留 123。\"}"}]}]}`))
				case "/api/enrichment/presentations/complete":
					if body["formatted_content"] != "## 保留 123。" || body["lease_token"] != "lease" {
						t.Error("invalid commit")
					}
					_, _ = w.Write([]byte(`{}`))
				case "/api/enrichment/presentations/fail":
					_, _ = w.Write([]byte(`{}`))
				default:
					t.Error("unexpected endpoint", r.URL.Path)
				}
			}))
			defer server.Close()
			runner := Runner{Config: Config{WorkerURL: server.URL, WorkerToken: "worker", BaseURL: server.URL, APIKey: "formatter", Model: "fixture", DailyLimit: 1}, Client: &http.Client{Timeout: time.Second}}
			claimed, err := runner.Once(t.Context())
			if !claimed || (err != nil) != fail {
				t.Fatalf("claimed=%v err=%v", claimed, err)
			}
			if len(calls) != 3 {
				t.Fatalf("unexpected calls %v", calls)
			}
		})
	}
}
