package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func liveDataset(t *testing.T) Dataset {
	t.Helper()
	d := Dataset{Name: "live-fixture", Split: "train"}
	for _, id := range []string{"a", "b"} {
		item := sample(id, gold([]string{"llm"}, "method", "try"))
		item.Provenance = ProvenanceSynthetic
		item.Material = &classify.Evidence{Primary: "Synthetic language model method " + id, Coverage: "complete"}
		var err error
		item.SourceHash, err = HashMaterial(*item.Material)
		if err != nil {
			t.Fatal(err)
		}
		d.Samples = append(d.Samples, item)
	}
	return d
}
func liveOptions() LiveOptions {
	return LiveOptions{Enabled: true, MaxSamples: 2, MaxCalls: 2, MaxTokens: 2 * jev113InputCeiling, Timeout: time.Second, Model: "jev-1.13.0"}
}
func TestLiveUsesActualHTTPAndPreservesWireIdentity(t *testing.T) {
	source := exportFixture(t)
	var calls atomic.Int64
	var lastRequest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var err error
		lastRequest, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": source.runs[0].Answers, "usage": map[string]int{"input_tokens": 123, "output_tokens": 45}})
	}))
	defer server.Close()
	client, err := classify.NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), exportCatalog())
	if err != nil {
		t.Fatal(err)
	}
	var records []CallRecord
	result, err := RunLive(context.Background(), liveDataset(t), client, liveOptions(), func(r CallRecord) error { records = append(records, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || calls.Load() != 2 || len(records) != 4 || result.InputTokens != 246 || result.ReservedInputTokens != 131072 {
		t.Fatalf("bad live accounting: %+v", result)
	}
	if string(lastRequest) != string(records[2].Request) || records[3].Raw.EvidenceHash != records[3].WireStateHash {
		t.Fatal("saved request differs from actual production wire state")
	}
	if len(result.Dataset.Prediction) != 2 || records[0].Phase != "reserved" || records[1].Phase != "finished" {
		t.Fatal("missing journal lifecycle")
	}
}
func TestLivePreflightAndOptInMakeZeroCalls(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	client, err := classify.NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), exportCatalog())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"disabled", "calls", "samples", "tokens", "model", "recorder", "material"} {
		t.Run(kind, func(t *testing.T) {
			options := liveOptions()
			d := liveDataset(t)
			recorder := func(CallRecord) error { return nil }
			switch kind {
			case "disabled":
				options.Enabled = false
			case "calls":
				options.MaxCalls = 1
			case "samples":
				options.MaxSamples = 1
			case "tokens":
				options.MaxTokens = 131071
			case "model":
				options.Model = "jev-latest"
			case "recorder":
				recorder = nil
			case "material":
				d.Samples[1].Material = nil
			}
			if _, err := RunLive(context.Background(), d, client, options, recorder); err == nil {
				t.Fatal("unsafe live plan accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("preflight sent a request")
	}
}
func TestLiveStopsAfterOneFailureWithoutRetry(t *testing.T) {
	for _, kind := range []string{"http", "usage", "drift", "ceiling", "journal"} {
		t.Run(kind, func(t *testing.T) {
			fixture := exportFixture(t)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if kind == "http" {
					w.WriteHeader(503)
					return
				}
				response := map[string]any{"model": "jev-1.13.0", "answers": fixture.runs[0].Answers, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2}}
				if kind == "usage" {
					delete(response, "usage")
				}
				if kind == "drift" {
					response["model"] = "jev-new"
				}
				if kind == "ceiling" {
					response["usage"] = map[string]int{"input_tokens": 65537, "output_tokens": 2}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client, err := classify.NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), exportCatalog())
			if err != nil {
				t.Fatal(err)
			}
			result, err := RunLive(context.Background(), liveDataset(t), client, liveOptions(), func(r CallRecord) error {
				if kind == "journal" && r.Phase == "reserved" {
					return errors.New("disk unavailable")
				}
				return nil
			})
			if err == nil || result.Completed || len(result.Dataset.Prediction) != 0 {
				t.Fatal("failure accepted")
			}
			expected := int64(1)
			if kind == "journal" {
				expected = 0
			}
			if calls.Load() != expected || result.ReservedInputTokens != expected*65536 {
				t.Fatalf("unbounded calls or lost reservation: %+v", result)
			}
		})
	}
}
func TestLiveCancellationBeforeReservationIsFree(t *testing.T) {
	client, err := classify.NewClient("https://example.invalid", "fixture", "jev-1.13.0", nil, exportCatalog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := RunLive(ctx, liveDataset(t), client, liveOptions(), func(CallRecord) error { t.Fatal("canceled work reserved"); return nil })
	if !errors.Is(err, context.Canceled) || result.Calls != 0 {
		t.Fatal("cancellation ignored")
	}
}
