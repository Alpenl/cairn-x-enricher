package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestLocalWorkerStoredQuestionReuse(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language model engineering", Active: true}, {ID: "eval", Label: "Eval", Description: "Benchmark methods", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Description: "Procedure", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try later", Active: true}}}
	const pinned = "jev-1.13.0"
	var sent [][]string
	drift := false
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			Questions map[string]struct {
				Type     string                     `json:"type"`
				Criteria map[string]json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(422)
			return
		}
		ids := []string{}
		answers := map[string]any{}
		for id, q := range body.Questions {
			ids = append(ids, id)
			if q.Type == "noul" {
				answers[id] = map[string]any{"type": "noul", "noul": .9}
				continue
			}
			options := []string{}
			for option := range q.Criteria {
				options = append(options, option)
			}
			sort.Strings(options)
			distribution := map[string]float64{}
			for _, option := range options {
				distribution[option] = 0
			}
			distribution[options[0]] = 1
			answers[id] = map[string]any{"type": "choice", "choice": options[0], "probabilities": distribution, "confidence": 1}
		}
		sort.Strings(ids)
		sent = append(sent, ids)
		model := pinned
		if drift {
			model = "jev-1.14.0"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "answers": answers, "usage": map[string]int{"input_tokens": len(ids) * 10, "output_tokens": len(ids)}})
	}))
	defer provider.Close()
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "Language model benchmark procedure.", OriginalLanguage: "en", ContextText: "quoted unrelated text", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	makeProcessor := func(model string) (*classify.Client, *processor.Processor) {
		client, err := classify.NewClient(provider.URL, "fixture", model, provider.Client(), catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := queue.PutQuestionSpec(ctx, client.Spec()); err != nil {
			t.Fatal(err)
		}
		postJSON(ctx, t, base+"/api/enrichment/classifications/target", token, map[string]any{"spec_id": client.SpecID(), "spec_hash": client.Spec().SemanticHash, "taxonomy_version": catalog.Version, "policy_version": classify.PolicyVersion, "requested_model": model, "protocol": "v2"})
		p := processor.NewStaged(queue, nil, client, catalog.Version, model, slog.New(slog.NewJSONHandler(&log, nil)), 1)
		p.SetPartialReuse(true)
		return client, p
	}
	retry := func() {
		postJSON(ctx, t, fmt.Sprintf("%s/api/enrichment/classifications/%d/retry", base, id), token, map[string]any{})
	}
	run := func(p *processor.Processor) {
		t.Helper()
		done, failed, err := p.RunClassifications(ctx, 1)
		if err != nil || done != 1 || failed != 0 {
			t.Fatalf("run done=%d failed=%d err=%v logs=%s", done, failed, err, log.String())
		}
	}
	latest := func() cairn.StoredRun {
		t.Helper()
		runs, err := queue.GetRuns(ctx, id)
		if err != nil || len(runs) == 0 {
			t.Fatalf("runs: %v %v", runs, err)
		}
		return runs[len(runs)-1]
	}
	decode := func(run cairn.StoredRun) classify.RawJudgments {
		t.Helper()
		stored, err := queue.GetQuestionSpec(ctx, run.SpecID)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := classify.DecodeSpec(stored.Payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := run.DecodeJudgments(spec)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	client, p := makeProcessor(pinned)
	run(p)
	first := latest()
	raw := decode(first)
	if first.SourceHash == "" || first.EvidenceSnapshotID == 0 || raw.WireState == "" || len(raw.Calls) != 1 || len(raw.QuestionHashes) != len(client.Spec().Questions) || len(sent) != 1 {
		t.Fatalf("first provenance incomplete: %+v", first)
	}
	totalQuestions := len(sent[0])
	// A semantic change alters exactly one provider question.
	catalog.Topics[0].Description += "; exclude model benchmarks unless substantive engineering is discussed"
	client, p = makeProcessor(pinned)
	run(p)
	second := latest()
	raw = decode(second)
	if len(sent) != 2 || len(sent[1]) != 1 || sent[1][0] != "topic_llm" || len(raw.Reused) != totalQuestions-1 || len(raw.Calls) != 1 {
		t.Fatalf("not question-level reuse: requests=%v raw=%+v", sent, raw)
	}
	for _, qid := range raw.Reused {
		if raw.ReusedFrom[qid] != first.ID {
			t.Fatal("reuse origin lost")
		}
	}
	var usage map[string]int
	if err := json.Unmarshal(second.Usage, &usage); err != nil || usage["input_tokens"] != 10 {
		t.Fatalf("old usage charged again: %s", second.Usage)
	}
	// Display-only changes keep the spec; explicit reclassification reuses all.
	specID := client.SpecID()
	catalog.Topics[0].Label = "Renamed display"
	client, p = makeProcessor(pinned)
	if client.SpecID() != specID {
		t.Fatal("display rename changed semantics")
	}
	retry()
	run(p)
	third := latest()
	if third.Attempt != 1 {
		t.Fatalf("run attempt used revision instead of claim count: %d", third.Attempt)
	}
	raw = decode(third)
	if len(sent) != 2 || len(raw.Calls) != 0 || len(raw.Reused) != totalQuestions {
		t.Fatal("display-only change invoked provider")
	}
	if err := json.Unmarshal(third.Usage, &usage); err != nil || usage["input_tokens"] != 0 {
		t.Fatalf("zero-call reuse usage=%s", third.Usage)
	}
	for _, qid := range raw.Reused {
		if raw.ReusedFrom[qid] != second.ID {
			t.Fatal("zero-call origin missing")
		}
	}
	policy := client.Policy()
	policy.Version += "-offline"
	policy.TopicAccept = .95
	if _, err := classify.Decide(raw, policy); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal("threshold replay called provider")
	}
	exported, err := evaluation.ExportDataset(ctx, queue, evaluation.ExportOptions{LinkIDs: []int64{id}})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Samples[0].SourceHash != third.SourceHash || exported.Prediction[0].Evaluation == nil || exported.Prediction[0].Evaluation.EvidenceHash != raw.EvidenceHash {
		t.Fatal("export lost historical source/wire identity")
	}
	// A later snapshot must not be retroactively attributed to an old run.
	updatedSource := source
	updatedSource.ContextText += " A newly archived continuation."
	newSnapshot := processor.EvidenceSnapshot(updatedSource, time.Now().Add(time.Second))
	postJSON(ctx, t, fmt.Sprintf("%s/api/v2/links/%d/evidence", base, id), token, map[string]any{"snapshot": newSnapshot})
	exported, err = evaluation.ExportDataset(ctx, queue, evaluation.ExportOptions{LinkIDs: []int64{id}})
	if err != nil || exported.Samples[0].SourceHash != third.SourceHash {
		t.Fatal("export adopted latest evidence identity")
	}
	beforeSourceChange := len(sent)
	retry()
	run(p)
	if len(sent) != beforeSourceChange+1 || len(sent[len(sent)-1]) != totalQuestions || latest().SourceHash == third.SourceHash {
		t.Fatal("new source was not inferred under its own identity")
	}
	// Persist old consumer shapes through the actual API. They remain unknown,
	// and the normal processor makes one full call, never a speculative partial.
	for index, coverage := range []string{"complete", "partial"} {
		old := latest()
		postJSON(ctx, t, fmt.Sprintf("%s/api/v2/links/%d/runs", base, id), token, map[string]any{"operation_key": fmt.Sprintf("legacy-reuse-%d", index), "content_revision": old.ContentRevision, "spec_id": old.SpecID, "spec_hash": old.SpecHash, "target_generation": old.TargetGeneration, "requested_model": old.RequestedModel, "resolved_model": old.ResolvedModel, "policy_version": old.PolicyVersion, "policy": json.RawMessage(old.Policy), "answers": json.RawMessage(old.Answers), "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}, "coverage": coverage})
		before := len(sent)
		retry()
		run(p)
		if len(sent) != before+1 || len(sent[len(sent)-1]) != totalQuestions {
			t.Fatalf("unknown/partial history %s: before=%d after=%d last=%v", coverage, before, len(sent), sent[len(sent)-1])
		}
	}
	// Aliases never inherit concrete-model cache identity without a new call.
	_, p = makeProcessor("jev-latest")
	before := len(sent)
	run(p)
	if len(sent) != before+1 || len(sent[len(sent)-1]) != totalQuestions {
		t.Fatal("alias fallback incorrect")
	}
	retry()
	run(p)
	if len(sent) != before+2 {
		t.Fatal("alias reused a previous resolution")
	}
	// An explicitly pinned client can consume a recorded same concrete version.
	_, p = makeProcessor(pinned)
	before = len(sent)
	run(p)
	if len(sent) != before {
		t.Fatal("same pinned model could not reuse recorded resolution")
	}
	prior := latest()
	// Drift after one new question is a failure, not a reason to pay for all
	// questions again or save a mixed complete run.
	catalog.Topics[0].Description += "; another boundary clarification"
	_, p = makeProcessor(pinned)
	drift = true
	before = len(sent)
	done, failed, err := p.RunClassifications(ctx, 1)
	if err == nil || done != 0 || failed != 0 || len(sent) != before+1 || len(sent[len(sent)-1]) != 1 || latest().ID != prior.ID {
		t.Fatalf("drift retried or committed: calls=%v done=%d failed=%d err=%v", sent, done, failed, err)
	}
	if !strings.Contains(log.String(), "provider_calls") || !strings.Contains(log.String(), "jev-1.14.0") {
		t.Fatal("failed paid attempt trace lost")
	}
	t.Logf("production store/read/reuse: initial=%d changed=1 display=0 policy=0; legacy/partial/alias bounded full; drift=1 and no fallback; total requests=%d", totalQuestions, len(sent))
}
