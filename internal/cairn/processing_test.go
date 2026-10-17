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
	"time"
)

func processingIntent(action string) ProcessingIntent {
	v := ProcessingIntent{Version: 1, OperationKey: "durable-operation", Fence: strings.Repeat("a", 64), Action: action,
		ExpectedContentRevision: 3, TargetGeneration: 0}
	if action == "save_primary" {
		text := "Human primary 🙂\nKeep whitespace. "
		v.Text = &text
	}
	return v
}

func processingStatusFixture() string {
	return fmt.Sprintf(`{"version":1,"id":4,"fence":%q,"content_revision":3,"target_generation":0,"max_text_bytes":100000,"model_calls":0,
"stages":{"source":{"state":"available"},"reading":{"state":"result_unknown","output_state":"unversioned"},"classification":{"state":"failed"},"human":{"state":"recorded"}},
"actions":{"save_primary":{"allowed":false,"blocked_reason":"primary_already_present","model_calls_now":0,"may_call_model_later":true,"queues":["reading","classification"]},
"retry_reading":{"allowed":false,"blocked_reason":"provider_result_unknown","model_calls_now":0,"may_call_model_later":true,"queues":["reading"]},
"retry_classification":{"allowed":true,"blocked_reason":null,"model_calls_now":0,"may_call_model_later":true,"queues":["classification"]}}}`, strings.Repeat("a", 64))
}

func processingReceiptFixture(action string) string {
	status, revision := "queued", 3
	if action == "save_primary" {
		status, revision = "source_saved", 4
	}
	return fmt.Sprintf(`{"version":1,"id":4,"operation_key":"durable-operation","action":%q,"status":%q,"content_revision":%d,
"source_hash":%q,"target_generation":0,"classification_queued":%t,"reading_queued":%t,"model_calls":0,"replayed":false}`,
		action, status, revision, strings.Repeat("b", 64), action != "retry_reading", action != "retry_classification")
}

func TestProcessingClientBoundedContractAndReceipt(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		responseBody := processingStatusFixture()
		if r.URL.Path != "/api/v2/links/4/processing" || r.Header.Get(ProcessingCapability) != "1" || r.Header.Get("Authorization") != "Bearer internal" {
			t.Error("wrong path, capability or authentication")
		}
		if r.Method == "POST" {
			var v ProcessingIntent
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil || !v.Valid() {
				t.Error("durable intent changed in transport")
			}
			responseBody = processingReceiptFixture(v.Action)
		}
		w.Header().Set(ProcessingCapability, "1")
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()
	client := NewClient(server.URL, "internal", server.Client())
	ctx := t.Context()
	if payload, err := client.GetProcessing(ctx, 4); err != nil || string(payload) != processingStatusFixture() {
		t.Fatalf("status contract failed: %v", err)
	}
	for _, action := range []string{"save_primary", "retry_reading", "retry_classification"} {
		if payload, err := client.SubmitProcessing(ctx, 4, processingIntent(action)); err != nil || string(payload) != processingReceiptFixture(action) {
			t.Fatalf("%s receipt failed: %v", action, err)
		}
	}
	if requests.Load() != 4 {
		t.Fatal("unexpected request fan-out")
	}
}

func TestProcessingClientRefusesUnboundOrMalformedSuccess(t *testing.T) {
	base := processingReceiptFixture("retry_classification")
	for _, change := range []struct{ old, next string }{
		{`"id":4`, `"id":5`}, {`"version":1`, `"version":2`}, {`"operation_key":"durable-operation"`, `"operation_key":"foreign"`},
		{`"action":"retry_classification"`, `"action":"retry_reading"`}, {`"content_revision":3`, `"content_revision":4`},
		{`"target_generation":0`, `"target_generation":1`}, {`"target_generation":0,`, ""},
		{`"classification_queued":true`, `"classification_queued":false`}, {`"reading_queued":false`, `"reading_queued":true`},
		{`"model_calls":0`, `"model_calls":1`}, {`"replayed":false`, `"replayed":null`}, {`"status":"queued"`, `"status":"completed"`},
	} {
		t.Run(change.old, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set(ProcessingCapability, "1")
				_, _ = io.WriteString(w, strings.Replace(base, change.old, change.next, 1))
			}))
			defer server.Close()
			if _, err := NewClient(server.URL, "internal", server.Client()).SubmitProcessing(t.Context(), 4, processingIntent("retry_classification")); err == nil {
				t.Fatal("accepted an unbound or incomplete receipt")
			}
			if requests.Load() != 1 {
				t.Fatal("automatically retried an unknown write")
			}
		})
	}
	for _, change := range []struct{ old, next string }{
		{`"source":{"state":"available"},`, ""}, {`"state":"available"`, `"state":""`}, {`"model_calls":0`, `"model_calls":1`},
		{`"blocked_reason":null`, `"blocked_reason":"blocked"`}, {`"model_calls_now":0`, `"model_calls_now":1`},
		{`"queues":["classification"]`, `"queues":["reading"]`}, {`"may_call_model_later":true`, `"may_call_model_later":false`},
	} {
		if validProcessingStatus([]byte(strings.Replace(processingStatusFixture(), change.old, change.next, 1))) {
			t.Fatalf("accepted misleading status: %s", change.old)
		}
	}
}

func TestProcessingClientRejectsLocallyAndNeverFollowsRedirectsOrRetriesUnknownWrite(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/must-not-follow", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewClient(server.URL, "internal", server.Client())
	invalid := []ProcessingIntent{}
	v := processingIntent("retry_classification")
	v.Text = new(string)
	invalid = append(invalid, v)
	v = processingIntent("save_primary")
	text := strings.Repeat("界", 33334)
	v.Text = &text
	invalid = append(invalid, v)
	v = processingIntent("save_primary")
	text = strings.Repeat("\x00", 100000) // UTF-8 text fits; encoded JSON does not.
	v.Text = &text
	invalid = append(invalid, v)
	v = processingIntent("retry_classification")
	v.Fence = "not-a-hash"
	invalid = append(invalid, v)
	v = processingIntent("retry_classification")
	v.Action = "arbitrary-target-management"
	invalid = append(invalid, v)
	for _, intent := range invalid {
		if _, err := client.SubmitProcessing(t.Context(), 4, intent); !errors.Is(err, ErrInvalidProcessing) {
			t.Fatalf("invalid local intent was not a known refusal: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid intent reached the network")
	}
	if _, err := client.SubmitProcessing(t.Context(), 4, processingIntent("retry_classification")); err == nil || calls.Load() != 1 {
		t.Fatal("followed a redirect or accepted an unmarked response")
	}
	for _, status := range []int{200, 400, 409, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"state_conflict"}`)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "internal", server.Client()).SubmitProcessing(t.Context(), 4, processingIntent("retry_classification"))
			var api *APIError
			if err == nil || errors.As(err, &api) {
				t.Fatal("unmarked legacy response became confirmed success/refusal")
			}
		})
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	if _, err := client.GetProcessing(ctx, 4); err == nil {
		t.Fatal("ignored cancelled context")
	}
}
