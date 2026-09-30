package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStoredProvenanceUsesRecordedWireAndRejectsTampering(t *testing.T) {
	var count int32
	var ids [][]string
	server := reuseServer(t, &count, &ids)
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "jev-latest", server.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), Input{OriginalText: "evidence", Note: "private note"})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := json.Marshal(result.Answers)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(result.RawJudgments)
	if err != nil {
		t.Fatal(err)
	}
	restore := func(blob []byte) (RawJudgments, error) {
		return RestoreStoredJudgments(client.Spec(), result.RequestedModel, result.Model, answers, result.Coverage, blob)
	}
	raw, err := restore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if raw.MetadataVersion != 1 || len(raw.Calls) != 1 || raw.Calls[0].UsageMissing || raw.WireState == "" || raw.EvidenceHash != sha256Hex([]byte(raw.WireState)) {
		t.Fatal("provenance lost")
	}
	legacy, err := restore(nil)
	if err != nil || legacy.MetadataVersion != 0 || legacy.EvidenceHash != "" || len(legacy.QuestionHashes) != 0 {
		t.Fatal("legacy identity fabricated")
	}
	for _, kind := range []string{"state", "hash", "question", "model", "answers", "calls", "usage", "call_usage", "source"} {
		t.Run(kind, func(t *testing.T) {
			var changed RawJudgments
			if err := json.Unmarshal(metadata, &changed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "state":
				changed.WireState += " "
			case "hash":
				changed.EvidenceHash = "wrong"
			case "question":
				for id := range changed.QuestionHashes {
					changed.QuestionHashes[id] = "wrong"
					break
				}
			case "model":
				changed.ResolvedModel = "different"
			case "answers":
				delete(changed.Judgments, "topic_llm")
			case "calls":
				changed.Calls = nil
			case "usage":
				changed.Usage = json.RawMessage(`{"input_tokens":99,"output_tokens":1}`)
			case "call_usage":
				changed.Calls[0].UsageMissing = true
			case "source":
				changed.Reused = []string{"topic_llm"}
				changed.ReusedFrom = map[string]int64{"topic_llm": 999}
			}
			blob, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restore(blob); err == nil {
				t.Fatal("tampered provenance accepted")
			}
		})
	}
}

// Use the real response decoder but switch the concrete model between chunks.
// A later chunk is billed and retained in calls; its answers must not enter the
// first model's judgments, nor make the aggregate falsely complete.
func TestBatchedModelDriftExcludesForeignAnswersAndRetainsUsage(t *testing.T) {
	var ordinaryCalls int32
	var ids [][]string
	ordinary := reuseServer(t, &ordinaryCalls, &ids)
	defer ordinary.Close()
	var calls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		ordinary.Config.Handler.ServeHTTP(recorder, r)
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			response["model"] = "jev-1.13.0"
		} else {
			response["model"] = "jev-1.14.0"
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer proxy.Close()
	client, err := NewClient(proxy.URL, "fixture", "jev-latest", proxy.Client(), testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := client.EvaluateBatched(context.Background(), Input{OriginalText: "evidence"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Coverage != "partial" || len(raw.Calls) != int(calls.Load()) || raw.ResolvedModel != "jev-1.13.0" || len(raw.Missing) == 0 {
		t.Fatalf("dishonest batch metadata: %+v", raw)
	}
	firstIDs := map[string]bool{}
	for _, id := range raw.Calls[0].QuestionIDs {
		firstIDs[id] = true
	}
	for id := range raw.Judgments {
		if !firstIDs[id] {
			t.Fatal("foreign model answer was merged")
		}
	}
	input, output, ok := tokenUsage(raw.Usage)
	if !ok || input != 5*calls.Load() || output != calls.Load() {
		t.Fatal("drifted call cost omitted")
	}
}
