package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func recoveryFixture(t *testing.T) (evaluation.Dataset, string) {
	t.Helper()
	var dataset evaluation.Dataset
	readJSON(t, "../reference-v1/frozen/train.json", &dataset)
	// Keep a second sample unattempted to prove export excludes the paid sample.
	dataset.Samples = dataset.Samples[:2]
	var catalog taxonomy.Catalog
	readJSON(t, "../reference-v1/taxonomy.json", &catalog)
	client, err := classify.NewClient("https://offline.invalid", "fixture", "jev-1.13.0", nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := client.BuildProviderRequest(classify.Input{Evidence: dataset.Samples[0].Material})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Questions map[string]struct {
			Type     string         `json:"type"`
			Criteria map[string]any `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	answers := map[string]any{}
	for id, q := range request.Questions {
		if q.Type == "noul" {
			answers[id] = map[string]any{"type": "noul", "noul": 0.1}
			continue
		}
		distribution := map[string]float64{}
		for option := range q.Criteria {
			distribution[option] = 0
		}
		distribution["none"] = 1
		answers[id] = map[string]any{"type": "choice", "choice": "none", "confidence": 1, "probabilities": distribution}
	}
	response, err := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 123, "output_tokens": 45}})
	if err != nil {
		t.Fatal(err)
	}
	wire := savedWire{Request: body, Response: string(response), ResponseBytes: len(response), Status: 200}
	transport := &savedTransport{wire: wire}
	client, err = classify.NewClient("https://offline.invalid", "fixture", "jev-1.13.0", &http.Client{Transport: transport}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	one := dataset
	one.Samples = one.Samples[:1]
	options := evaluation.LiveOptions{Enabled: true, MaxSamples: 1, MaxCalls: 1, MaxTokens: 65536, Timeout: time.Second, Model: "jev-1.13.0"}
	var records []evaluation.CallRecord
	result, err := evaluation.RunLive(context.Background(), one, client, options, func(entry evaluation.CallRecord) error { records = append(records, entry); return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Model output was already paid for but an earlier decoder rejected it.
	records[1].Raw = nil
	records[1].Error = "old parser rejected valid response"
	records[1].UsageKnown = false
	records[1].InputTokens = 0
	records[1].OutputTokens = 0
	hash, err := evaluation.HashReference(dataset)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := privateJSON(root, "plan.json", savedPlan{Plan: result.Plan, SourceReferenceHash: hash, Catalog: catalog}); err != nil {
		t.Fatal(err)
	}
	writeLines(t, filepath.Join(root, "calls.ndjson"), records)
	writeLines(t, filepath.Join(root, "wire.ndjson"), []savedWire{wire})
	return dataset, root
}
func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
func writeLines[T any](t *testing.T, path string, values []T) {
	t.Helper()
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	for _, value := range values {
		if err := json.NewEncoder(file).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRecoveryReusesExactWireWithoutNetworkAndKeepsOriginalFailure(t *testing.T) {
	dataset, root := recoveryFixture(t)
	recovered, pending, result, err := recoverWires(dataset, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if result.ModelCalls != 0 || result.OriginalCalls != 1 || result.InputTokens != 123 || result.UsageMissingCalls != 0 || result.Recovered != 1 || len(result.ArtifactHashes) != 3 {
		t.Fatalf("bad recovery accounting: %+v", result)
	}
	if len(recovered.Prediction) != 1 || len(pending.Samples) != 1 || pending.Samples[0].SampleID != dataset.Samples[1].SampleID {
		t.Fatal("recovery may repeat a paid sample")
	}
	if result.Calls[0].OriginalError == "" || result.Calls[0].RecoveryError != "" || result.Calls[0].Raw == nil {
		t.Fatal("original failure or recovered judgment lost")
	}
	if _, _, _, err := recoverWires(dataset, []string{root, root}); err == nil {
		t.Fatal("double-counted a paid sample")
	}
}
func TestRecoveryRejectsIdentityMismatchAndAmbiguousJournal(t *testing.T) {
	for _, kind := range []string{"reference", "spec", "wire", "source", "state", "incomplete", "truncated", "order", "retry"} {
		t.Run(kind, func(t *testing.T) {
			dataset, root := recoveryFixture(t)
			switch kind {
			case "reference":
				dataset.Samples[0].Language = "changed"
			case "spec":
				var plan savedPlan
				readJSON(t, filepath.Join(root, "plan.json"), &plan)
				plan.Plan.SpecHash = "different"
				writeSingle(t, filepath.Join(root, "plan.json"), plan)
			case "wire":
				data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "wire.ndjson")))
				if err != nil {
					t.Fatal(err)
				}
				wires, err := decodeLines[savedWire](data)
				if err != nil {
					t.Fatal(err)
				}
				wires[0].Request = json.RawMessage(`{}`)
				writeLines(t, filepath.Join(root, "wire.ndjson"), wires)
			default:
				data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "calls.ndjson")))
				if err != nil {
					t.Fatal(err)
				}
				records, err := decodeLines[evaluation.CallRecord](data)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "source":
					records[0].SourceHash = "bad"
				case "state":
					records[0].WireStateHash = "bad"
				case "incomplete":
					records = records[:1]
				case "order":
					records[0], records[1] = records[1], records[0]
				case "retry":
					records[0].Retry = true
				case "truncated":
					records = append(records, records[0])
				}
				writeLines(t, filepath.Join(root, "calls.ndjson"), records)
			}
			if _, _, _, err := recoverWires(dataset, []string{root}); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
		})
	}
}
func writeSingle(t *testing.T, path string, value any) { t.Helper(); writeLines(t, path, []any{value}) }
func TestRecoveryPreservesFailedAttemptAndUsageWithoutRetry(t *testing.T) {
	dataset, root := recoveryFixture(t)
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "wire.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	wires, err := decodeLines[savedWire](data)
	if err != nil {
		t.Fatal(err)
	}
	wires[0].Status = 503
	writeLines(t, filepath.Join(root, "wire.ndjson"), wires)
	recovered, pending, result, err := recoverWires(dataset, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Prediction) != 0 || len(pending.Samples) != 1 || result.OriginalCalls != 1 || result.InputTokens != 123 || result.Calls[0].RecoveryError == "" {
		t.Fatal("failed paid attempt was forgotten or queued for retry")
	}
}
