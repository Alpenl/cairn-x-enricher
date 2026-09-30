package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func TestFreezeRejectsChangedHistoricalSpec(t *testing.T) {
	root := t.TempDir()
	directory, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	for _, name := range []string{"scenarios.json", "taxonomy.json", "baseline-spec.json"} {
		data, err := os.ReadFile(filepath.Clean(filepath.Join("../reference-v1", name)))
		if err != nil {
			t.Fatal(err)
		}
		if name == "baseline-spec.json" {
			data = []byte(strings.ReplaceAll(string(data), "original_text", "another_field"))
		}
		if err := directory.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(root, "changed-freeze")
	if err := generate(root, output); err == nil || !strings.Contains(err.Error(), "baseline spec identity changed") {
		t.Fatalf("modified baseline accepted: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("invalid historical spec created a new freeze")
	}
}

func TestFreezeIsReproducibleAndGroupDisjoint(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	if err := generate("../reference-v1", first); err != nil {
		t.Fatal(err)
	}
	if err := generate("../reference-v1", second); err != nil {
		t.Fatal(err)
	}
	datasets := []evaluation.Dataset{}
	total := 0
	for _, name := range []string{"train", "dev", "holdout", "manifest"} {
		left, err := os.ReadFile(filepath.Clean(filepath.Join(first, name+".json")))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Clean(filepath.Join(second, name+".json")))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Clean(filepath.Join("../reference-v1/frozen", name+".json")))
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(committed) {
			t.Fatal("committed freeze differs from generator")
		}
		if string(left) != string(right) {
			t.Fatal("freeze is not reproducible")
		}
		if name == "manifest" {
			continue
		}
		var d evaluation.Dataset
		if err := json.Unmarshal(left, &d); err != nil {
			t.Fatal(err)
		}
		datasets = append(datasets, d)
		total += len(d.Samples)
	}
	if total != 240 {
		t.Fatal(total)
	}
	if err := evaluation.ValidateSplits(datasets); err != nil {
		t.Fatal(err)
	}
	if err := generate("../reference-v1", first); err == nil {
		t.Fatal("existing freeze was overwritten")
	}
}
