package classify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// This opt-in export uses only a checked current catalog and synthetic source.
// It exercises the actual Go request/restore paths for independent Worker
// completion tests; it never reads private bookmarks or calls a live provider.
func TestCurrentCatalogGranularityProtocolFixtures(t *testing.T) {
	path := os.Getenv("CAIRN_GRANULAR_CATALOG_PATH")
	if path == "" {
		t.Skip("current vocabulary contract fixture is opt-in")
	}
	//nolint:gosec // Explicit local opt-in test vocabulary; no network or credential input.
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(encoded, &catalog); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var sent [][]string
	server := granularProvider(t, &calls, &sent)
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Spec().Questions) != 55 {
		t.Fatalf("unexpected target question count: %d", len(client.Spec().Questions))
	}
	input := Input{URL: "https://x.com/synthetic/status/4", OriginalText: "写真制作流程和可复用 Skill。"}
	full, err := client.Classify(context.Background(), input)
	if err != nil || full.RawJudgments.MetadataVersion != 1 || len(full.RawJudgments.Calls) != 2 {
		t.Fatalf("full catalog contract failed: %v", err)
	}
	previousCatalog := catalog
	previousCatalog.Version = "2026-09-30.2"
	previousCatalog.Topics = nil
	for _, term := range catalog.Topics {
		if !term.Specific() {
			previousCatalog.Topics = append(previousCatalog.Topics, term)
		}
	}
	previousClient, err := NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), previousCatalog)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := previousClient.Classify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	previous.RawJudgments.SourceRunID = 1
	beforeCalls := calls.Load()
	reused, err := client.ClassifyReusing(context.Background(), input, &previous.RawJudgments, previous.RawJudgments.BatchSemantics)
	if err != nil || calls.Load()-beforeCalls != 1 || len(reused.RawJudgments.Reused) != 31 || len(reused.RawJudgments.Calls[0].QuestionIDs) != 24 {
		t.Fatalf("actual catalog did not reuse core31/infer24: %v", err)
	}
	boundedClient, err := client.WithCandidatePolicy(CandidatePolicy{Version: CandidatePolicyVersion, MaxQuestions: 32})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := boundedClient.Classify(context.Background(), input)
	if err != nil || bounded.RawJudgments.MetadataVersion != 2 || len(bounded.RawJudgments.Judgments) > 32 || len(bounded.RawJudgments.Calls) != 1 {
		t.Fatalf("actual bounded candidate contract failed: %v", err)
	}
	for _, result := range []Result{full, reused, bounded} {
		answers, _ := json.Marshal(result.Answers)
		metadata, _ := json.Marshal(result.RawJudgments)
		if _, err := RestoreStoredJudgments(client.Spec(), result.RequestedModel, result.Model, answers, result.Coverage, metadata); err != nil {
			t.Fatalf("actual current catalog restore failed: %v", err)
		}
	}
	out := os.Getenv("CAIRN_GRANULAR_FIXTURE_DIR")
	if out == "" {
		return
	}
	for name, result := range map[string]Result{"go-current-full-v1.json": full, "go-current-reuse-v1.json": reused, "go-current-candidate-v2.json": bounded} {
		fixture, err := json.MarshalIndent(map[string]any{
			"catalog": catalog, "spec": client.Spec(), "spec_hash": client.Spec().SemanticHash, "result": result,
			"input": input, "previous_spec": previousClient.Spec(), "previous_result": previous,
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Explicit opt-in private fixture directory; only fixed synthetic filenames.
		if err := os.WriteFile(filepath.Join(out, name), fixture, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
