package classify

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"testing"
)

func densityNoul(dimension, term string, p float64) RawJudgment {
	return RawJudgment{QuestionID: dimension + "_" + term, Kind: QuestionNoul, Dimension: dimension, TermID: term, Noul: &p}
}

func TestDensityMinimumUsesOnlySupportedFunctionNouls(t *testing.T) {
	for _, tc := range []struct {
		name      string
		judgments []RawJudgment
		functions []string
		total     int
		supported string
	}{
		{"two strong labels need no fill", []RawJudgment{rawNoul("ai_coding", .8), densityNoul("resource_kinds", "reference", .8), densityNoul("content_functions", "method", .79)}, nil, 2, ""},
		{"support boundary fills one", []RawJudgment{rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .65)}, []string{"method"}, 2, "method"},
		{"weak support leaves sparse result", []RawJudgment{rawNoul("ai_coding", .9), densityNoul("content_functions", "method", math.Nextafter(.65, 0))}, nil, 1, ""},
		{"no subject is invented", []RawJudgment{rawNoul("ai_coding", .5), densityNoul("content_functions", "case", .71), densityNoul("content_functions", "method", .65)}, []string{"case", "method"}, 2, "method"},
		{"highest supported function wins", []RawJudgment{rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .7), densityNoul("content_functions", "case", .79)}, []string{"case"}, 2, "case"},
		{"strong function keeps normal reason", []RawJudgment{rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .8), densityNoul("content_functions", "case", .79)}, []string{"method"}, 2, ""},
		{"negative and personal signals cannot fill", []RawJudgment{rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .2), densityNoul("affordances", "quote", .99), rawChoice("use", "contra", map[string]float64{"contra": .99, "none": .01})}, nil, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := completeRaw(tc.judgments...)
			before, _ := json.Marshal(raw)
			got, err := Decide(raw, DefaultPolicy())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.ContentFunctions, tc.functions) || len(got.Topics)+len(got.ResourceKinds)+len(got.ContentFunctions) != tc.total {
				t.Fatalf("unexpected primary tags: %+v", got)
			}
			if tc.supported != "" {
				decision := decisionFor(got, "content_functions", tc.supported)
				if decision.Verdict != VerdictAccepted || decision.Reason != "density_support" {
					t.Fatalf("support audit lost: %+v", decision)
				}
				if decision.Probability != *raw.Judgments["content_functions_"+tc.supported].Noul {
					t.Fatal("changed original probability")
				}
			}
			if tc.name == "strong function keeps normal reason" && decisionFor(got, "content_functions", "method").Reason == "density_support" {
				t.Fatal("marked a strong acceptance as density support")
			}
			after, _ := json.Marshal(raw)
			if !bytes.Equal(before, after) {
				t.Fatal("mutated raw judgments")
			}
		})
	}
	raw := completeRaw(rawNoul("ai_coding", .9))
	raw.Coverage, raw.Missing = "partial", []string{"content_functions_method"}
	got, err := Decide(raw, DefaultPolicy())
	if err != nil || len(got.ContentFunctions) != 0 || !slices.Contains(got.Incomplete, "missing:content_functions_method") {
		t.Fatalf("missing judgment manufactured support: %+v %v", got, err)
	}
	missing := densityNoul("content_functions", "method", .7)
	missing.Noul = nil
	if _, err := Decide(completeRaw(missing), DefaultPolicy()); err == nil {
		t.Fatal("missing Noul probability was accepted")
	}
}

func TestDensityMaximumRetainsTopicAndResourceAndAuditsExclusions(t *testing.T) {
	raw := completeRaw(rawNoul("weak_topic", .8), rawNoul("strong_topic", .9), densityNoul("resource_kinds", "reference", .81),
		densityNoul("content_functions", "method", .95), densityNoul("content_functions", "case", .94), densityNoul("content_functions", "data", .93),
		densityNoul("content_functions", "opinion", .92), densityNoul("content_functions", "research", .91), densityNoul("affordances", "quote", .99))
	got, err := Decide(raw, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Topics, []string{"strong_topic"}) || !slices.Equal(got.ResourceKinds, []string{"reference"}) ||
		!slices.Equal(got.ContentFunctions, []string{"method", "case", "data"}) || !slices.Equal(got.Affordances, []string{"quote"}) {
		t.Fatalf("cap discarded a preserved dimension or wrong candidates: %+v", got)
	}
	for _, dropped := range []struct {
		dimension, term string
		p               float64
	}{{"topics", "weak_topic", .8}, {"content_functions", "opinion", .92}, {"content_functions", "research", .91}} {
		decision := decisionFor(got, dropped.dimension, dropped.term)
		if decision.Verdict != VerdictAbstained || decision.Reason != "label_limit" || decision.Probability != dropped.p {
			t.Fatalf("cap lost exclusion audit: %+v", decision)
		}
	}
	boundary, err := Decide(completeRaw(rawNoul("ai_coding", .8), densityNoul("resource_kinds", "reference", .8),
		densityNoul("content_functions", "case", .8), densityNoul("content_functions", "data", .8), densityNoul("content_functions", "method", .8)), DefaultPolicy())
	if err != nil || len(boundary.Topics)+len(boundary.ResourceKinds)+len(boundary.ContentFunctions) != 5 {
		t.Fatalf("five-tag boundary changed: %+v %v", boundary, err)
	}
	for _, decision := range boundary.Decisions {
		if decision.Reason == "label_limit" {
			t.Fatal("excluded a tag at the maximum boundary")
		}
	}
}

func TestDensityTiesProduceStableAutomaticIdentity(t *testing.T) {
	raw := completeRaw(rawNoul("b", .9), rawNoul("a", .9), densityNoul("resource_kinds", "software", .9), densityNoul("resource_kinds", "skill", .9),
		densityNoul("content_functions", "opinion", .9), densityNoul("content_functions", "method", .9), densityNoul("content_functions", "data", .9), densityNoul("content_functions", "case", .9))
	var previous []byte
	for range 128 {
		got, err := Decide(raw, DefaultPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Topics, []string{"a"}) || !slices.Equal(got.ResourceKinds, []string{"skill"}) || !slices.Equal(got.ContentFunctions, []string{"case", "data", "method"}) {
			t.Fatalf("unstable dimension/term ties: %+v", got)
		}
		encoded, _ := json.Marshal(AutomaticFromProposals(got))
		if previous != nil && !bytes.Equal(encoded, previous) {
			t.Fatal("same judgments changed automatic payload identity")
		}
		previous = encoded
	}
	filled, err := Decide(completeRaw(rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .7), densityNoul("content_functions", "case", .7)), DefaultPolicy())
	if err != nil || !slices.Equal(filled.ContentFunctions, []string{"case"}) {
		t.Fatalf("support ties were not term-stable: %+v %v", filled, err)
	}
}

func TestDensityRunsBeforeHumanOverrides(t *testing.T) {
	proposal, err := Decide(completeRaw(rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .7)), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	rejected := Resolve(proposal, []Override{{Field: "content_functions", Term: "method", Action: OverrideReject, Revision: 1}})
	if len(rejected.ContentFunctions) != 0 || len(rejected.Topics) != 1 {
		t.Fatalf("refilled a human rejection: %+v", rejected)
	}
	empty := Resolve(proposal, []Override{{Field: "topics", Action: OverrideSetEmpty, Revision: 1}, {Field: "content_functions", Action: OverrideSetEmpty, Revision: 2}})
	if len(empty.Topics)+len(empty.ContentFunctions) != 0 || !empty.Empty.Topics || !empty.Empty.ContentFunctions {
		t.Fatalf("refilled a human empty state: %+v", empty)
	}
	var additions []Override
	for index, term := range []string{"one", "two", "three", "four", "five", "six"} {
		additions = append(additions, Override{Field: "topics", Term: term, Action: OverrideAccept, Revision: int64(index + 1)})
	}
	added := Resolve(proposal, additions)
	if len(added.Topics) != 7 || len(added.ContentFunctions) != 1 {
		t.Fatalf("density cap removed human additions: %+v", added)
	}
}

func TestDensityValidationAndHistoricalPolicyIdentity(t *testing.T) {
	legacyJSON := []byte(`{"version":"jev-policy-v3","calibrated":false,"topic_accept":0.8,"topic_reject":0.2,"choice_accept":0.65,"choice_margin":0.15,"max_display_topics":3,"max_effective_topics":64,"allow_alias_drift":false,"block_personal_use":true}`)
	legacy, err := DecodePolicy(legacyJSON)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(legacy)
	if !bytes.Equal(encoded, legacyJSON) {
		t.Fatalf("old policy JSON identity changed: %s", encoded)
	}
	for _, version := range []string{"jev-policy-v2", "jev-policy-v3"} {
		for _, field := range []string{"minimum", "maximum", "support"} {
			policy := legacy
			policy.Version = version
			policy.BlockPersonalUse = version != "jev-policy-v2"
			switch field {
			case "minimum":
				policy.MinPrimaryTags = 2
			case "maximum":
				policy.MaxPrimaryTags = 5
			case "support":
				policy.FunctionSupportAccept = .65
			}
			if policy.Validate() == nil {
				t.Fatalf("new %s semantics reused old %s identity", field, version)
			}
		}
	}
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.BlockPersonalUse = false }, func(p *Policy) { p.MinPrimaryTags = 0 }, func(p *Policy) { p.MaxPrimaryTags = 1 },
		func(p *Policy) { p.MinPrimaryTags = 6 }, func(p *Policy) { p.MaxPrimaryTags = 65 }, func(p *Policy) { p.FunctionSupportAccept = math.NaN() },
		func(p *Policy) { p.FunctionSupportAccept = p.TopicReject }, func(p *Policy) { p.FunctionSupportAccept = 1.1 },
	} {
		policy := DefaultPolicy()
		mutate(&policy)
		if policy.Validate() == nil {
			t.Fatalf("invalid v4 policy accepted: %+v", policy)
		}
	}
	raw := completeRaw(rawNoul("ai_coding", .9), densityNoul("resource_kinds", "reference", .88), densityNoul("content_functions", "method", .95),
		densityNoul("content_functions", "case", .94), densityNoul("content_functions", "data", .93), densityNoul("content_functions", "opinion", .92))
	old, err := Decide(raw, legacy)
	if err != nil || len(old.Topics)+len(old.ResourceKinds)+len(old.ContentFunctions) != 6 || old.PolicyVersion != "jev-policy-v3" {
		t.Fatalf("historical v3 was capped: %+v %v", old, err)
	}
	oldBytes, _ := json.Marshal(old)
	before, after, changed, err := Replay(raw, legacy, DefaultPolicy())
	beforeBytes, _ := json.Marshal(before)
	if err != nil || !bytes.Equal(beforeBytes, oldBytes) || len(after.Topics)+len(after.ResourceKinds)+len(after.ContentFunctions) != 5 || len(changed) == 0 {
		t.Fatalf("replay changed historical baseline or missed v4 cap: %v %+v", err, after)
	}
	sparse, _ := Decide(completeRaw(rawNoul("ai_coding", .9), densityNoul("content_functions", "method", .7)), legacy)
	if len(sparse.ContentFunctions) != 0 || sparse.PolicyVersion != "jev-policy-v3" {
		t.Fatal("v3 acquired density support")
	}
}
