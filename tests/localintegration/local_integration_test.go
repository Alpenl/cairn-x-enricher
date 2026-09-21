// Package localintegration drives the real Go client and classifier against a
// real local Worker/D1. It is skipped unless CAIRN_WORKER_URL is set, so the
// normal `go test ./...` stays hermetic. The orchestrating script is
// tests/local-integration/run.sh, which starts wrangler dev with the real
// migrations and a local D1.
package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func workerURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("CAIRN_WORKER_URL")
	if base == "" {
		t.Skip("CAIRN_WORKER_URL is not set; run tests/local-integration/run.sh")
	}
	return base
}

// providerContractServer is a local stand-in for the paid TypeSafe endpoint. It
// validates the official request shape (map of typed questions, structured
// state) and answers from the compiled spec, so the real classifier and the
// real Worker are exercised without a paid call.
func providerContractServer(t *testing.T, spec classify.QuestionSpec) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("provider request is not an object: %v", err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		var questions map[string]struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw["questions"], &questions); err != nil || len(questions) == 0 {
			t.Errorf("questions is not the official map shape: %s", raw["questions"])
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		for id, question := range questions {
			if question.Type != "noul" && question.Type != "choice" && question.Type != "score" {
				t.Errorf("question %s has no official type: %q", id, question.Type)
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
		}
		answers := map[string]any{}
		for _, question := range spec.Questions {
			switch question.Kind {
			case classify.QuestionNoul:
				answers[question.ID] = map[string]any{"type": "noul", "noul": 0.93}
			case classify.QuestionChoice:
				options := question.AnswerOptions()
				distribution := map[string]float64{}
				for index, option := range options {
					if index == 0 {
						distribution[option] = 0.95
					} else {
						distribution[option] = 0.05 / float64(len(options)-1)
					}
				}
				answers[question.ID] = map[string]any{
					"type": "choice", "choice": options[0], "probabilities": distribution, "confidence": 0.8,
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-pinned-local", "answers": answers,
			"usage": map[string]int{"input_tokens": 123, "output_tokens": 45},
		})
	}))
}

func TestLocalWorkerFullLifecycle(t *testing.T) {
	base := workerURL(t)
	appToken := envOr("CAIRN_APP_TOKEN", "app")
	enricherToken := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	catalog := taxonomy.Catalog{
		Version: "2026-09-20.1",
		Topics: []taxonomy.Term{
			{ID: "llm", Label: "LLM", Description: "大语言模型", Active: true},
			{ID: "eval", Label: "评估", Description: "模型评估", Active: true},
		},
		Forms:            []taxonomy.Term{{ID: "method", Label: "方法", Description: "方法", Active: true}},
		Uses:             []taxonomy.Term{{ID: "try", Label: "待试", Description: "待试", Active: true}},
		ContentFunctions: []taxonomy.Term{{ID: "method", Label: "方法", Description: "方法", Active: true}},
		Carriers:         []taxonomy.Term{{ID: "single_post", Label: "单帖", Description: "单帖", Active: true}},
		Affordances:      []taxonomy.Term{{ID: "practice", Label: "可实践", Description: "可实践", Active: true}},
	}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "local-key", "jev-latest", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	queue := cairn.NewClient(base, enricherToken, &http.Client{Timeout: 30 * time.Second})

	// 1. A normal new bookmark arrives through the App API.
	id := createLink(t, base, appToken)
	lease := claimEnrichmentJob(t, base, enricherToken, id)
	source := enrich.Source{
		OriginalText:     "A practical guide to evaluating large language models.",
		OriginalLanguage: "en", ContextText: "A related comment", RelatedLinks: []string{},
		ImageURLs: []string{}, Model: "local-fetch",
	}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatalf("save source: %v", err)
	}
	// 2. The evidence snapshot is part of the production path, not a hand seed.
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatalf("submit evidence: %v", err)
	}
	// 3. The authoritative target is activated for this consumer's immutable
	// spec, exactly as an operator would.
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatalf("register question spec: %v", err)
	}
	switchTarget(t, base, enricherToken, classifier)

	// 4. Real v2 handshake -> claim. The bound policy/model must come from the
	// server target, not from the consumer's claim body (F02).
	job, err := queue.ClaimClassification(ctx, classifier.SpecID(), catalog.Version, "jev-latest")
	if err != nil {
		t.Fatalf("claim classification: %v", err)
	}
	if job == nil {
		t.Fatal("claim returned no job")
	}
	if job.SpecID != classifier.SpecID() {
		t.Fatalf("job spec = %q, want %q", job.SpecID, classifier.SpecID())
	}
	if job.OriginalText != source.OriginalText {
		t.Fatalf("job lost the stored source: %+v", job)
	}

	// 5. Real Evaluate -> Decide -> Complete against the real Worker.
	result, err := classifier.Classify(ctx, job.Input)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if err := queue.CompleteClassification(ctx, job, result); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// 6. The run is stored, replayable, and carries the real usage.
	runs, err := queue.GetRuns(ctx, id)
	if err != nil {
		t.Fatalf("get runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	var usage map[string]int
	if err := json.Unmarshal(runs[0].Usage, &usage); err != nil || usage["input_tokens"] != 123 {
		t.Fatalf("usage was not preserved: %s (%v)", runs[0].Usage, err)
	}
	storedSpec, err := queue.GetQuestionSpec(ctx, runs[0].SpecID)
	if err != nil {
		t.Fatalf("get question spec: %v", err)
	}
	spec, err := classify.DecodeSpec(storedSpec.Payload)
	if err != nil {
		t.Fatalf("decode stored spec: %v", err)
	}
	raw, err := classify.DecodeStoredJudgments(spec, runs[0].RequestedModel, runs[0].ResolvedModel, runs[0].Answers, runs[0].Coverage)
	if err != nil {
		t.Fatalf("decode stored judgments: %v", err)
	}
	historical, err := classify.DecodePolicy(runs[0].Policy)
	if err != nil {
		t.Fatalf("decode historical policy: %v", err)
	}
	replayPolicy := historical
	replayPolicy.Version = historical.Version + "+local-replay"
	replayPolicy.TopicAccept = 0.5
	replayPolicy.TopicReject = 0.1
	_, after, _, err := classify.Replay(raw, historical, replayPolicy)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(after.Topics) != 2 {
		t.Fatalf("replay topics = %v, want both topics", after.Topics)
	}

	// 7. The effective view is derived from the stored decision, and a human
	// rejection survives a later decision that re-proposes the same tag (F07).
	view, err := queue.GetV2Selection(ctx, id)
	if err != nil {
		t.Fatalf("get v2 selection: %v", err)
	}
	if !view.Available || len(view.Selection.Topics) == 0 {
		t.Fatalf("selection is empty after the production completion: %+v", view)
	}
	effective, err := queue.GetV2Effective(ctx, id)
	if err != nil {
		t.Fatalf("get v2 effective: %v", err)
	}
	if !bytes.Contains(effective, []byte(`"projected":true`)) {
		t.Fatalf("effective view is not derived from a decision: %s", effective)
	}
	if _, err := queue.ApplyV2Override(ctx, id, cairn.V2Override{
		Field: "topics", Term: "llm", Action: "reject", OperationKey: fmt.Sprintf("local-reject-%d", id),
		ExpectedRevision: &view.Revision,
	}); err != nil {
		t.Fatalf("apply override: %v", err)
	}
	// A controlled replay appends a decision over the existing run; it never
	// creates a new model run and never touches the source.
	if err := queue.SubmitDecision(ctx, id, map[string]any{
		"operation_key":    fmt.Sprintf("local-replay-%d", id),
		"run_ids":          []int64{runs[0].ID},
		"policy_version":   replayPolicy.Version,
		"policy":           replayPolicy,
		"spec_id":          runs[0].SpecID,
		"requested_model":  runs[0].RequestedModel,
		"content_revision": runs[0].ContentRevision,
		"automatic":        classify.AutomaticFromProposals(after),
	}); err != nil {
		t.Fatalf("controlled replay commit: %v", err)
	}
	afterReplay, err := queue.GetV2Selection(ctx, id)
	if err != nil {
		t.Fatalf("get selection after replay: %v", err)
	}
	for _, topic := range afterReplay.Selection.Topics {
		if topic == "llm" {
			t.Fatalf("human rejection was revived by the replay: %+v", afterReplay.Selection)
		}
	}
}

// TestLocalWorkerVersionCompetition is the real SC01/SC02 regression: after a
// job is completed, alternating legacy and v2 consumers must never re-claim it,
// and an in-flight completion must lose to a target switch.
func TestLocalWorkerVersionCompetition(t *testing.T) {
	base := workerURL(t)
	appToken := envOr("CAIRN_APP_TOKEN", "app")
	enricherToken := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, enricherToken, &http.Client{Timeout: 30 * time.Second})

	// The lifecycle test may have left a v2 target active; point a new
	// generation back at the legacy protocol so the old consumer contract is
	// exercised on its real endpoint.
	legacyTarget := map[string]any{
		"spec_id": "legacy", "spec_hash": "legacy", "taxonomy_version": "2026-09-20.1",
		"policy_version": "legacy-policy", "requested_model": "legacy-model", "protocol": "legacy",
	}
	body, _ := json.Marshal(legacyTarget)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/enrichment/classifications/target", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+enricherToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("activate legacy target: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("activate legacy target status = %d", response.StatusCode)
	}
	id := createLink(t, base, appToken)
	lease := claimEnrichmentJob(t, base, enricherToken, id)
	source := enrich.Source{OriginalText: "Version competition source.", OriginalLanguage: "en",
		ContextText: "", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "local"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatalf("save source: %v", err)
	}
	// Complete one job under the legacy protocol, which is what an old client
	// uses.
	legacyClaim := legacyClaim(t, base, enricherToken)
	if legacyClaim == nil {
		t.Fatal("legacy claim returned no job")
	}
	legacyComplete(t, base, enricherToken, id, legacyClaim)
	// 20 rounds of A/B alternation must never hand the completed job out again.
	for round := 0; round < 20; round++ {
		body, _ := json.Marshal(map[string]any{
			"protocol": "legacy", "taxonomy_version": "2026-09-20.1",
			"policy_version": fmt.Sprintf("policy-%d", round%2), "model": "legacy-model",
		})
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/enrichment/classifications/claim", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+enricherToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("round %d claim: %v", round, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("round %d re-claimed a completed job: HTTP %d", round, response.StatusCode)
		}
	}
	// An in-flight completion loses to a target switch: the old result must not
	// overwrite the new projection.
	switchTarget(t, base, enricherToken, mustClassifier(t))
	rejected := queue.CompleteClassification(ctx, legacyClaim, classify.Result{
		Classification: taxonomy.Classification{Selection: taxonomy.Selection{Topics: []string{"eval"}, Form: "case", Use: "try"},
			Entities: []string{}, TaxonomyVersion: "2026-09-20.1", DiscardedTags: []string{}},
		Model: "legacy-model", PolicyVersion: "legacy-policy", Answers: map[string]classify.RawAnswer{},
	})
	if rejected == nil {
		t.Fatal("a completion after a target switch must be rejected")
	}
	view, err := queue.GetV2Selection(ctx, id)
	if err != nil {
		t.Fatalf("get selection: %v", err)
	}
	for _, topic := range view.Selection.Topics {
		if topic == "eval" {
			t.Fatal("the stale completion overwrote the current projection")
		}
	}
}

func mustClassifier(t *testing.T) *classify.Client {
	t.Helper()
	catalog := taxonomy.Catalog{
		Version: "2026-09-20.1",
		Topics:  []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "大语言模型", Active: true}},
		Forms:   []taxonomy.Term{{ID: "method", Label: "方法", Description: "方法", Active: true}},
		Uses:    []taxonomy.Term{{ID: "try", Label: "待试", Description: "待试", Active: true}},
	}
	classifier, err := classify.NewClient("http://127.0.0.1:1", "key", "jev-latest", nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return classifier
}

// legacyComplete posts the pre-v2 completion payload an old consumer sends, on
// the original endpoint. It is deliberately raw so the test exercises the exact
// legacy contract rather than the new client's helper.
func legacyComplete(t *testing.T, base, token string, id int64, job *cairn.ClassificationJob) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision,
		"result": map[string]any{
			"model": "legacy-model", "policy_version": "legacy-policy",
			"answers": map[string]any{},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
			"classification": map[string]any{
				"topics": []string{"llm"}, "form": "method", "use": "try", "uncertainty": false,
				"taxonomy_version": "2026-09-20.1", "why_suggestion": "", "entities": []string{}, "discarded_tags": []string{},
			},
		},
	})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		fmt.Sprintf("%s/api/enrichment/classifications/%d/complete", base, id), bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("legacy complete: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("legacy complete status = %d: %s", response.StatusCode, payload)
	}
}

// legacyClaim posts the pre-v2 claim payload an old consumer sends.
func legacyClaim(t *testing.T, base, token string) *cairn.ClassificationJob {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"protocol": "legacy", "taxonomy_version": "2026-09-20.1",
		"policy_version": "legacy-policy", "model": "legacy-model",
	})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/enrichment/classifications/claim", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("legacy claim: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("legacy claim status = %d", response.StatusCode)
	}
	var job cairn.ClassificationJob
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return &job
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustSpec(t *testing.T, catalog taxonomy.Catalog) classify.QuestionSpec {
	t.Helper()
	spec, err := classify.CompileSpec(catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func createLink(t *testing.T, base, token string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"url": "https://x.com/local/status/1", "note": ""})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/links", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		t.Fatalf("create link status = %d", response.StatusCode)
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == 0 {
		t.Fatal("create link returned no id")
	}
	return payload.ID
}

func claimEnrichmentJob(t *testing.T, base, token string, id int64) string {
	t.Helper()
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fmt.Sprintf("%s/api/enrichment/jobs/%d/claim", base, id), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("claim enrichment job: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim enrichment job status = %d", response.StatusCode)
	}
	var payload struct {
		LeaseToken string `json:"lease_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.LeaseToken
}

func switchTarget(t *testing.T, base, token string, classifier *classify.Client) {
	t.Helper()
	spec := classifier.Spec()
	body, _ := json.Marshal(map[string]any{
		"spec_id": spec.SpecID, "spec_hash": spec.SemanticHash,
		"taxonomy_version": spec.TaxonomyVersion, "policy_version": classify.PolicyVersion,
		"requested_model": "jev-latest", "protocol": "v2",
	})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/enrichment/classifications/target", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("switch target: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("switch target status = %d", response.StatusCode)
	}
}
