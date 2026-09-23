package classify

import (
	"math"
	"testing"
)

func TestProbabilityMapInclusiveRoundingBoundary(t *testing.T) {
	// Reduced from the saved jev-1.13.0 response to synthetic training sample
	// auto-incidental-ai-5. Preserve the wire values rather than normalizing.
	distribution := map[string]float64{"case": 0.34, "none": 0.27, "tool": 0, "thread": 0.01, "method": 0.01, "data": 0, "longform": 0.16, "opinion": 0.20}
	keys := []string{"case", "none", "tool", "thread", "method", "data", "longform", "opinion"}
	for offset := range keys {
		order := append(append([]string{}, keys[offset:]...), keys[:offset]...)
		if err := ValidateProbabilityMap(distribution, order); err != nil {
			t.Fatalf("valid rounded response rejected in order %v: %v", order, err)
		}
	}
	for _, total := range []float64{0.99, 1.01} {
		if err := ValidateProbabilityMap(map[string]float64{"a": 0.5, "b": total - 0.5}, []string{"a", "b"}); err != nil {
			t.Fatalf("inclusive boundary %v: %v", total, err)
		}
	}
	for _, value := range []float64{0.48999999, 0.51000001, -0.01, math.NaN(), math.Inf(1)} {
		if err := ValidateProbabilityMap(map[string]float64{"a": 0.5, "b": value}, []string{"a", "b"}); err == nil {
			t.Fatalf("invalid distribution accepted: %v", value)
		}
	}
}
