package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func materialIntentFixture() ManualMaterialIntent {
	return ManualMaterialIntent{Version: 1, OperationKey: "stable-intent", ExpectedContentRevision: 3, EvidenceSnapshotID: 8,
		SourceHash: strings.Repeat("a", 64), TargetGeneration: 2, Kind: "image_text", SourceRef: "known-image", Text: "Human image text 🙂"}
}
func materialOptionsFixture() string {
	return fmt.Sprintf(`{"version":1,"id":4,"source":{"content_revision":3,"evidence_snapshot_id":8,"source_hash":%q,"target_generation":2},"can_submit":true,"blocked_reason":null,"max_text_bytes":100000,"options":[{"kind":"image_text","source_ref":"known-image","role":"legacy_unknown"}]}`, strings.Repeat("a", 64))
}
func materialReceiptFixture(replayed bool) string {
	return fmt.Sprintf(`{"version":1,"id":4,"operation_key":"stable-intent","request_id":"request-id","status":"material_saved","content_revision":4,"evidence_snapshot_id":9,"source_hash":%q,"changed":true,"classification_queued":true,"replayed":%t,"model_calls":0}`, strings.Repeat("b", 64), replayed)
}

func TestManualMaterialKeepsExactIntentAndAcceptsOnlyBoundReceipt(t *testing.T) {
	intent := materialIntentFixture()
	// HTML is ordinary source material. Escaping it must not turn a valid
	// 100,000 byte text into a 600,000 byte upstream request.
	intent.Text = strings.Repeat("<>&", 30000)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/api/v2/links/4/manual-material" || r.Header.Get(manualMaterialCapability) != "1" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("lost exact capability or identity")
		}
		w.Header().Set(manualMaterialCapability, "1")
		if r.Method == "GET" {
			_, _ = io.WriteString(w, materialOptionsFixture())
			return
		}
		payload, _ := io.ReadAll(r.Body)
		var received ManualMaterialIntent
		if len(payload) > 128<<10 || json.Unmarshal(payload, &received) != nil || received != intent {
			t.Error("intent changed or grew outside wire budget")
		}
		_, _ = io.WriteString(w, materialReceiptFixture(n > 2))
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	if data, err := client.GetManualMaterial(context.Background(), 4); err != nil || string(data) != materialOptionsFixture() {
		t.Fatalf("options: %v", err)
	}
	for _, replayed := range []bool{false, true} {
		data, err := client.SubmitManualMaterial(context.Background(), 4, intent)
		if err != nil || string(data) != materialReceiptFixture(replayed) {
			t.Fatalf("receipt: %v", err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("request was automatically repeated")
	}
}

func TestManualMaterialRejectsMalformedOrForeignOptionsAndReceipts(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		post          bool
	}{
		{"foreign", strings.Replace(materialOptionsFixture(), `"id":4`, `"id":5`, 1), false},
		{"missing decision", strings.Replace(materialOptionsFixture(), `"can_submit":true,`, "", 1), false},
		{"invalid gap", strings.Replace(materialOptionsFixture(), `"image_text"`, `"semantic_uncertainty"`, 1), false},
		{"false primary", strings.Replace(materialOptionsFixture(), `"legacy_unknown"`, `"primary"`, 1), false},
		{"future version", strings.Replace(materialOptionsFixture(), `"version":1`, `"version":2`, 1), false},
		{"oversized", strings.Repeat(" ", 128<<10) + materialOptionsFixture(), false},
		{"invalid UTF8", strings.Replace(materialOptionsFixture(), "known-image", "bad\xff", 1), false},
		{"foreign operation", strings.Replace(materialReceiptFixture(false), "stable-intent", "other-intent", 1), true},
		{"foreign bookmark", strings.Replace(materialReceiptFixture(false), `"id":4`, `"id":5`, 1), true},
		{"wrong revision", strings.Replace(materialReceiptFixture(false), `"content_revision":4`, `"content_revision":6`, 1), true},
		{"unbound snapshot", strings.Replace(materialReceiptFixture(false), `"evidence_snapshot_id":9`, `"evidence_snapshot_id":8`, 1), true},
		{"false no-op", strings.Replace(materialReceiptFixture(false), `"changed":true`, `"changed":false`, 1), true},
		{"missing zero", strings.Replace(materialReceiptFixture(false), `,"model_calls":0`, "", 1), true},
		{"unexpected call", strings.Replace(materialReceiptFixture(false), `"model_calls":0`, `"model_calls":1`, 1), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set(manualMaterialCapability, "1")
				_, _ = io.WriteString(w, test.payload)
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture", server.Client())
			var err error
			if test.post {
				_, err = client.SubmitManualMaterial(context.Background(), 4, materialIntentFixture())
			} else {
				_, err = client.GetManualMaterial(context.Background(), 4)
			}
			if err == nil || calls.Load() != 1 {
				t.Fatal("malformed response was accepted or retried")
			}
		})
	}
}

func TestManualMaterialRefusesRedirectsAndRetainsUnknownWriteOutcome(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 307, 308} {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
		client := NewClient(origin.URL, "fixture", origin.Client())
		if _, err := client.GetManualMaterial(context.Background(), 4); err == nil {
			t.Fatal("redirect accepted")
		}
		if _, err := client.SubmitManualMaterial(context.Background(), 4, materialIntentFixture()); err == nil {
			t.Fatal("write redirect accepted")
		}
		origin.Close()
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		w.Header().Set(manualMaterialCapability, "1")
		_, _ = io.WriteString(w, materialReceiptFixture(true))
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture", server.Client())
	if _, err := client.SubmitManualMaterial(context.Background(), 4, materialIntentFixture()); err == nil || calls.Load() != 1 {
		t.Fatal("unknown result was retried automatically")
	}
	if _, err := client.SubmitManualMaterial(context.Background(), 4, materialIntentFixture()); err != nil || calls.Load() != 2 {
		t.Fatal("explicit same intent could not confirm receipt")
	}
}

func TestManualMaterialDistinguishesUnsupportedMissingAndConflict(t *testing.T) {
	for _, test := range []struct {
		status      int
		ack, code   string
		unsupported bool
	}{
		{404, "", "", true}, {405, "", "", true}, {501, "", "", true},
		{200, "", "", true}, {404, "1", "not_found", false}, {409, "1", "provider_result_unknown", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(manualMaterialCapability, test.ack)
			w.WriteHeader(test.status)
			_, _ = fmt.Fprintf(w, `{"error":%q}`, test.code)
		}))
		_, err := NewClient(server.URL, "fixture", server.Client()).GetManualMaterial(context.Background(), 4)
		server.Close()
		if test.unsupported {
			if !errors.Is(err, ErrV2Unsupported) {
				t.Fatal(err)
			}
		} else {
			var api *APIError
			if !errors.As(err, &api) || api.Code != test.code || api.StatusCode != test.status {
				t.Fatalf("lost error identity: %v", err)
			}
		}
	}
	client := NewClient("http://127.0.0.1:1", "fixture", nil)
	bad := materialIntentFixture()
	bad.Text = strings.Repeat("中", 33334)
	if _, err := client.SubmitManualMaterial(context.Background(), 4, bad); err == nil || strings.Contains(err.Error(), "connect") {
		t.Fatal("invalid text contacted upstream")
	}
	bad.Text = "prefix" + strings.Repeat("\u2028", 33000)
	if !bad.Valid() {
		t.Fatal("fixture must fit the source-text budget before JSON encoding")
	}
	if _, err := client.SubmitManualMaterial(context.Background(), 4, bad); !errors.Is(err, ErrInvalidManualMaterial) {
		t.Fatal("encoded overflow must be a known pre-send refusal")
	}
	if _, err := client.GetManualMaterial(context.Background(), 1<<53); err == nil || strings.Contains(err.Error(), "connect") {
		t.Fatal("invalid identity contacted upstream")
	}
}
