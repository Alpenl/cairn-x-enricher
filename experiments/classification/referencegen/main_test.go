package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

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
