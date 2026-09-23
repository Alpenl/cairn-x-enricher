package classify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// overrideVector is one shared human-override behaviour vector. The same file
// is checked into the Worker repository (worker/test/fixtures) and both
// resolvers must produce the expected view, so a human action can never mean
// one thing in the UI and another in replay (F11).
type overrideVector struct {
	Name      string `json:"name"`
	Automatic struct {
		Topics           []string `json:"topics"`
		ContentFunctions []string `json:"content_functions"`
		Carriers         []string `json:"carriers"`
		Affordances      []string `json:"affordances"`
		Form             string   `json:"form"`
		Use              string   `json:"use"`
		Entities         []string `json:"entities"`
	} `json:"automatic"`
	Overrides []struct {
		Field    string `json:"field"`
		Term     string `json:"term"`
		Action   string `json:"action"`
		Revision int64  `json:"revision"`
	} `json:"overrides"`
	Expected struct {
		Topics           []string `json:"topics"`
		ContentFunctions []string `json:"content_functions"`
		Carriers         []string `json:"carriers"`
		Affordances      []string `json:"affordances"`
		Form             string   `json:"form"`
		Use              string   `json:"use"`
		Entities         []string `json:"entities"`
	} `json:"expected"`
}

func TestSharedOverrideVectors(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "override-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []overrideVector `json:"vectors"`
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Vectors) == 0 {
		t.Fatal("the shared vector fixture is empty")
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			proposals := Proposals{
				Topics: vector.Automatic.Topics, ContentFunctions: vector.Automatic.ContentFunctions,
				Carriers: vector.Automatic.Carriers, Affordances: vector.Automatic.Affordances,
				Form: vector.Automatic.Form, Use: vector.Automatic.Use, Entities: vector.Automatic.Entities,
			}
			overrides := make([]Override, 0, len(vector.Overrides))
			for _, entry := range vector.Overrides {
				overrides = append(overrides, Override{
					Field: entry.Field, Term: entry.Term, Action: OverrideAction(entry.Action),
					Source: "human", Revision: entry.Revision,
				})
			}
			view := Resolve(proposals, overrides)
			got := struct {
				Topics           []string `json:"topics"`
				ContentFunctions []string `json:"content_functions"`
				Carriers         []string `json:"carriers"`
				Affordances      []string `json:"affordances"`
				Form             string   `json:"form"`
				Use              string   `json:"use"`
				Entities         []string `json:"entities"`
			}{view.Topics, view.ContentFunctions, view.Carriers, view.Affordances, view.Form, view.Use, view.Entities}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(vector.Expected)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("resolved view mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestRawAnswerRoundTripsThroughProviderShape is the F06 encoding regression:
// the default struct encoding used to nest noul as {"noul":{"noul":...}} while
// the decoder only accepted the flat provider shape, so a stored answer could
// not be decoded again.
func TestRawAnswerRoundTripsThroughProviderShape(t *testing.T) {
	probability := 0.93
	confidence := 0.8
	answers := map[string]RawAnswer{
		"topic_llm": {Type: TypeNoul, Noul: &NoulAnswer{Noul: &probability}},
		"form": {Type: TypeChoice, Choice: &ChoiceAnswer{Choice: "method",
			Probabilities: map[string]float64{"method": 0.95, "none": 0.05}}, Confidence: &confidence},
		"importance": {Type: TypeScore, Score: &ScoreAnswer{Score: 1.05,
			Legend:        map[string]string{"0": "low", "1": "medium", "2": "high"},
			Probabilities: map[string]float64{"0": 0.0, "1": 0.95, "2": 0.05}}},
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"noul":0.93`; !contains(string(encoded), want) {
		t.Fatalf("noul must be encoded flat, got %s", encoded)
	}
	decoded, err := DecodeAnswers(encoded)
	if err != nil {
		t.Fatalf("stored answers must decode again: %v (%s)", err, encoded)
	}
	if decoded["topic_llm"].Noul == nil || *decoded["topic_llm"].Noul.Noul != 0.93 {
		t.Fatalf("noul round-trip lost its value: %+v", decoded["topic_llm"])
	}
	if decoded["importance"].Score == nil || decoded["importance"].Score.Score != 1.05 {
		t.Fatalf("score round-trip lost its float position: %+v", decoded["importance"])
	}
	if decoded["importance"].Score.Legend["2"] != "high" {
		t.Fatalf("score legend round-trip lost order: %+v", decoded["importance"].Score.Legend)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for index := 0; index+len(needle) <= len(haystack); index++ {
			if haystack[index:index+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// completeAnswersFor builds a valid provider answer set for a compiled spec.
func completeAnswersFor(t *testing.T, spec QuestionSpec) []byte {
	t.Helper()
	answers := map[string]RawAnswer{}
	for _, question := range spec.Questions {
		switch question.Kind {
		case QuestionNoul:
			probability := 0.9
			answers[question.ID] = RawAnswer{Type: TypeNoul, Noul: &NoulAnswer{Noul: &probability}}
		case QuestionChoice:
			options := question.AnswerOptions()
			distribution := map[string]float64{}
			for index, option := range options {
				if index == 0 {
					distribution[option] = 0.95
				} else {
					distribution[option] = 0.05 / float64(len(options)-1)
				}
			}
			answers[question.ID] = RawAnswer{Type: TypeChoice,
				Choice: &ChoiceAnswer{Choice: options[0], Probabilities: distribution}}
		case QuestionScore:
			indices := question.AnswerOptions()
			distribution := map[string]float64{}
			for index, level := range indices {
				if index == 0 {
					distribution[level] = 1
				} else {
					distribution[level] = 0
				}
			}
			answers[question.ID] = RawAnswer{Type: TypeScore,
				Score: &ScoreAnswer{Score: 0, Legend: question.ScoreLegend(), Probabilities: distribution}}
		}
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestDecodeStoredJudgmentsUsesTheStoredSpec proves a replay reads the real
// question definition instead of guessing a dimension from the question id.
func TestDecodeStoredJudgmentsUsesTheStoredSpec(t *testing.T) {
	spec, err := CompileSpec(testCatalog(), false)
	if err != nil {
		t.Fatal(err)
	}
	answers := completeAnswersFor(t, spec)
	raw, err := DecodeStoredJudgments(spec, "jev-latest", "jev-1.13.0", answers, "complete")
	if err != nil {
		t.Fatal(err)
	}
	if raw.Judgments["topic_llm"].Dimension != "topic" || raw.Judgments["form"].Dimension != "form" {
		t.Fatalf("dimensions were not recovered from the spec: %+v", raw.Judgments)
	}
	if !raw.AliasDrift {
		t.Fatal("resolved/requested model mismatch must be recorded as drift")
	}
	// An answer set that does not match the spec is a contract error, not a
	// silently empty replay.
	if _, err := DecodeStoredJudgments(spec, "jev-latest", "jev", []byte(`{"topic_llm":{"type":"noul","noul":0.9}}`), "complete"); err == nil {
		t.Fatal("a partial answer set must not decode as a complete run")
	}
	// A run without a recoverable policy cannot be replayed under a default.
	if _, err := DecodePolicy(nil); err == nil {
		t.Fatal("an empty stored policy must fail explicitly")
	}
}

// TestReplayRefusesDefaultPolicySubstitution is the F06 baseline guard.
func TestReplayRefusesDefaultPolicySubstitution(t *testing.T) {
	spec, _ := CompileSpec(testCatalog(), false)
	answers := completeAnswersFor(t, spec)
	raw, err := DecodeStoredJudgments(spec, "jev-latest", "jev-latest", answers, "complete")
	if err != nil {
		t.Fatal(err)
	}
	oldPolicy := DefaultPolicy()
	oldPolicy.Version = ""
	if _, _, _, err := Replay(raw, oldPolicy, DefaultPolicy()); err == nil {
		t.Fatal("a replay without a historical policy version must fail")
	}
	// The full policy payload round-trips so a replay uses the real thresholds.
	payload, _ := json.Marshal(DefaultPolicy())
	decoded, err := DecodePolicy(payload)
	if err != nil || decoded.TopicAccept != DefaultPolicy().TopicAccept {
		t.Fatalf("policy round-trip failed: %+v (%v)", decoded, err)
	}
}
