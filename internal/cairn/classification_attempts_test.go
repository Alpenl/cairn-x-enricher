package cairn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func TestClassificationAttemptReceiptHasBoundIdentityAndCapability(t *testing.T) {
	for _, capability := range []bool{true, false} {
		t.Run(map[bool]string{true: "negotiated", false: "missing"}[capability], func(t *testing.T) {
			var keys []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/classification-attempts" || r.Header.Get("X-Cairn-Classification-Attempts") != "1" {
					t.Error("missing receipt capability")
				}
				var body struct {
					OperationKey string                  `json:"operation_key"`
					LinkID       int64                   `json:"link_id"`
					LeaseToken   string                  `json:"lease_token"`
					EvidenceHash string                  `json:"evidence_hash"`
					Calls        []classify.ProviderCall `json:"calls"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.LinkID != 4 || body.LeaseToken != "lease" || body.EvidenceHash != "evidence" || len(body.Calls) != 1 || body.Calls[0].ReservationKey != "actual-reservation" {
					t.Error("lost reservation/source binding")
				}
				keys = append(keys, body.OperationKey)
				if capability {
					w.Header().Set("X-Cairn-Classification-Attempts", "1")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"stored": true, "replayed": len(keys) > 1, "operation_key": body.OperationKey, "attempt_ids": []int{1}})
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture", server.Client())
			job := &ClassificationJob{ID: 4, LeaseToken: "lease", EvidenceHash: "evidence"}
			calls := []classify.ProviderCall{{ReservationKey: "actual-reservation", RequestHash: "request", UsageMissing: true, HTTPStatus: 0, ErrorClass: "timeout"}}
			for range 2 {
				err := client.SubmitClassificationAttempts(context.Background(), job, calls)
				if (err == nil) != capability {
					t.Fatalf("receipt cap=%v err=%v", capability, err)
				}
			}
			if keys[0] != keys[1] {
				t.Fatal("receipt retries used different operation identity")
			}
		})
	}
}
