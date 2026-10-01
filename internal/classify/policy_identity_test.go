package classify

import (
	"math"
	"testing"
)

func TestReplayPolicyIdentityChangesWithMeaningAndIsStableForRetry(t *testing.T) {
	a := DefaultPolicy()
	b := a
	b.TopicAccept = .75
	pa, ha, err := WithPolicyHashIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, hb, err := WithPolicyHashIdentity(b)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Version == pb.Version || ha == hb {
		t.Fatal("different thresholds share policy identity")
	}
	again, hash, err := WithPolicyHashIdentity(a)
	if err != nil || again != pa || hash != ha {
		t.Fatal("equal retry changed policy identity")
	}
	key1, _ := PolicyReplayOperationKey(4, []int64{2, 1}, ha, 1, "spec", "jev", "jev", 1)
	key2, _ := PolicyReplayOperationKey(4, []int64{1, 2}, ha, 1, "spec", "jev", "jev", 1)
	key3, _ := PolicyReplayOperationKey(4, []int64{1, 2}, hb, 1, "spec", "jev", "jev", 1)
	key4, _ := PolicyReplayOperationKey(4, []int64{1, 2}, ha, 1, "spec", "jev", "jev", 2)
	if key1 != key2 || key1 == key3 || key1 == key4 {
		t.Fatal("operation identity lost order stability, policy distinction or target binding")
	}
	invalid := a
	invalid.Version = "jev-policy-v3"
	if _, _, err := WithPolicyHashIdentity(invalid); err == nil {
		t.Fatal("renaming bypassed historical policy semantic guard")
	}
}

func TestReplayCanonicalScalarsMatchWorkerJSON(t *testing.T) {
	canonical, err := canonicalReplayJSON(map[string]any{"negative_zero": math.Copysign(0, -1), "tiny": 1e-7, "boundary": 1e-6,
		"html": "<>&\u2028\u2029", "large": 1e21, "nested": []float64{.8, 1e-8}})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"boundary\":0.000001,\"html\":\"<>&\u2028\u2029\",\"large\":1e+21,\"negative_zero\":0,\"nested\":[0.8,1e-8],\"tiny\":1e-7}"
	if string(canonical) != want {
		t.Fatalf("Worker scalar mismatch: %s", canonical)
	}
}
