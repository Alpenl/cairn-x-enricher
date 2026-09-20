package classification

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestSyntheticGoldenVector pins the report shape and the inconclusive rule for
// a committed synthetic fixture. It makes no network call.
func TestSyntheticGoldenVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/synthetic-dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset Dataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		t.Fatal(err)
	}
	if err := dataset.Validate(); err != nil {
		t.Fatal(err)
	}
	report, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	// The hash must be stable across runs so a report can cite the split.
	if len(report.DatasetHash) != 64 {
		t.Fatalf("dataset hash = %q", report.DatasetHash)
	}
	if report.SamplesWithGold != 3 || report.SamplesMissing != 0 {
		t.Fatalf("counts = %d/%d", report.SamplesWithGold, report.SamplesMissing)
	}
	// Topics are perfect; content_functions miss one label.
	byDimension := map[string]DimensionMetric{}
	for _, metric := range report.Dimensions {
		byDimension[metric.Dimension] = metric
	}
	if byDimension["topics"].F1 != 1 {
		t.Fatalf("topics F1 = %v, want 1", byDimension["topics"].F1)
	}
	if math.Abs(byDimension["content_functions"].Recall-2.0/3.0) > 1e-9 {
		t.Fatalf("content_functions recall = %v", byDimension["content_functions"].Recall)
	}
	if !report.Inconclusive {
		t.Fatal("a 3-sample fixture must be inconclusive")
	}
	decision := EvaluateGate(report, DefaultGate())
	if decision.Promote {
		t.Fatal("an inconclusive report must not promote")
	}
}

// TestGoldenVectorIsReproducible ensures scoring the same input twice yields
// byte-identical JSON, which the report pipeline depends on.
func TestGoldenVectorIsReproducible(t *testing.T) {
	raw, _ := os.ReadFile("testdata/synthetic-dataset.json")
	var dataset Dataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		t.Fatal(err)
	}
	first, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Score(dataset)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("scoring is not deterministic")
	}
}
