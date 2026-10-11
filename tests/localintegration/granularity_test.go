package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Exercise the shipping catalog across Go, real Worker/D1 and the same GET
// smoke used for production. Only TypeSafe's paid boundary is a local fixture.
func TestLocalWorkerTopicGranularity(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil || catalog.Version != "2026-10-02.1" {
		t.Fatalf("current catalog: %s %v", catalog.Version, err)
	}
	old := catalog
	old.Version, old.DefinitionVersion = "2026-09-30.2", 4
	old.Topics = nil
	for _, term := range catalog.Topics {
		if term.Granularity != "specific" {
			term.Granularity, term.Navigation, term.RecallTerms = "", false, nil
			old.Topics = append(old.Topics, term)
		}
	}
	var mu sync.Mutex
	var sent [][]string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct {
				Type     string                     `json:"type"`
				Criteria map[string]json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
			t.Error(decodeErr)
			w.WriteHeader(422)
			return
		}
		ids, answers := []string{}, map[string]any{}
		for id, question := range body.Questions {
			ids = append(ids, id)
			if question.Type == "noul" {
				p := .03
				switch id {
				case "topic_portrait_photography":
					p = .98
				case "topic_image_creation":
					p = .94
				case "resource_kind_skill":
					p = .95
				}
				answers[id] = map[string]any{"type": "noul", "noul": p}
			} else {
				options := []string{}
				for option := range question.Criteria {
					options = append(options, option)
				}
				sort.Strings(options)
				selected := options[0]
				if slices.Contains(options, "none") {
					selected = "none"
				}
				probabilities := map[string]float64{}
				for _, option := range options {
					probabilities[option] = 0
				}
				probabilities[selected] = 1
				answers[id] = map[string]any{"type": "choice", "choice": selected, "probabilities": probabilities, "confidence": 1}
			}
		}
		sort.Strings(ids)
		mu.Lock()
		sent = append(sent, ids)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": len(ids) * 10, "output_tokens": len(ids)}})
	}))
	defer provider.Close()
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	lease := claimEnrichmentJob(t, base, token, id)
	source := enrich.Source{OriginalText: "这份写真 Skill 使用图像模型制作人像照片。", OriginalLanguage: "zh", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := queue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	classifyOne := func(vocabulary taxonomy.Catalog, candidate, reuse bool) (cairn.StoredRun, classify.RawJudgments) {
		t.Helper()
		client, err := classify.NewClient(provider.URL, "fixture", "jev-1.13.0", provider.Client(), vocabulary)
		if err != nil {
			t.Fatal(err)
		}
		if candidate {
			if err := queue.ProbeCandidateManifestCapability(ctx); err != nil {
				t.Fatal(err)
			}
			client, err = client.WithCandidatePolicy(classify.CandidatePolicy{Version: classify.CandidatePolicyVersion, MaxQuestions: 32})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := queue.PutQuestionSpec(ctx, client.Spec()); err != nil {
			t.Fatal(err)
		}
		postJSON(ctx, t, base+"/api/enrichment/classifications/target", token, map[string]any{"spec_id": client.SpecID(), "spec_hash": client.Spec().SemanticHash, "taxonomy_version": vocabulary.Version, "policy_version": classify.PolicyVersion, "requested_model": "jev-1.13.0", "protocol": "v2"})
		postJSON(ctx, t, fmt.Sprintf("%s/api/enrichment/classifications/%d/retry", base, id), token, map[string]any{})
		p := processor.NewStaged(queue, nil, client, vocabulary.Version, "jev-1.13.0", slog.New(slog.NewJSONHandler(&logs, nil)), 1)
		p.SetPartialReuse(reuse)
		done, failed, err := p.RunClassifications(ctx, 1)
		if err != nil || done != 1 || failed != 0 {
			t.Fatalf("classification: %d/%d %v %s", done, failed, err, logs.String())
		}
		runs, err := queue.GetRuns(ctx, id)
		if err != nil || len(runs) == 0 {
			t.Fatalf("runs: %v", err)
		}
		run := runs[len(runs)-1]
		raw, err := run.DecodeJudgments(client.Spec())
		if err != nil {
			t.Fatal(err)
		}
		return run, raw
	}
	previous, priorRaw := classifyOne(old, false, false)
	if previous.SpecID != "classify-16e168873ee5" || len(priorRaw.Judgments) != 31 {
		t.Fatalf("historical production spec changed: %s %d", previous.SpecID, len(priorRaw.Judgments))
	}
	current, raw := classifyOne(catalog, false, true)
	if len(raw.Judgments) != 55 || len(raw.Reused) != 31 || len(raw.Calls) != 1 || len(raw.Calls[0].QuestionIDs) != 24 || current.EvidenceSnapshotID != previous.EvidenceSnapshotID || current.SourceHash != previous.SourceHash {
		t.Fatalf("31+24 incremental identity failed: reused=%d judgments=%d calls=%+v", len(raw.Reused), len(raw.Judgments), raw.Calls)
	}
	for _, q := range raw.Reused {
		if raw.ReusedFrom[q] != previous.ID || raw.QuestionHashes[q] != priorRaw.QuestionHashes[q] || !reflect.DeepEqual(raw.Judgments[q].Noul, priorRaw.Judgments[q].Noul) {
			t.Fatalf("old answer provenance changed: %s", q)
		}
	}
	_, bounded := classifyOne(catalog, true, false)
	if bounded.MetadataVersion != 2 || bounded.CandidateManifest == nil || len(bounded.CandidateManifest.SelectedQuestionIDs) != 32 || len(bounded.CandidateManifest.OmittedQuestionIDs) != 23 || bounded.Coverage != "complete" {
		t.Fatalf("candidate manifest did not survive Worker roundtrip: %+v", bounded.CandidateManifest)
	}
	if _, ok := bounded.Judgments["topic_whiteboard_animation"]; ok {
		t.Fatal("omitted topic became a judgment")
	}
	mu.Lock()
	sizes := []int{}
	for _, batch := range sent {
		sizes = append(sizes, len(batch))
	}
	mu.Unlock()
	if !reflect.DeepEqual(sizes, []int{31, 24, 32}) {
		t.Fatalf("actual provider batches: %v", sizes)
	}
	// The same shipping release verifier must work with populated real D1.
	share := os.Getenv("CAIRN_SHARE_ROOT")
	if share == "" {
		share = "../../../cairn-share"
	}
	module, err := filepath.Abs(filepath.Join(share, "scripts/release-smoke.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	script := `const {verifyRelease}=await import(process.env.SMOKE_MODULE); const result=await verifyRelease({token:process.env.SMOKE_TOKEN,base:process.env.SMOKE_BASE}); if(result.checks.length!==11) throw Error('wrong_check_count'); console.log('11 real GET contracts passed');`
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", script)
	command.Env = append(os.Environ(), "SMOKE_MODULE="+module, "SMOKE_TOKEN="+token, "SMOKE_BASE="+base)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("production smoke against local populated D1: %s %v", output, err)
	}
	// A second branch of the broad OR still matches; the third has no portrait.
	for index, topics := range [][]string{{"video_creation", "portrait_photography"}, {"video_creation"}} {
		created := postJSON(ctx, t, base+"/api/links", envOr("CAIRN_APP_TOKEN", "app"), map[string]any{"url": fmt.Sprintf("https://example.com/granularity/%d", index)})
		for _, topic := range topics {
			_, err := queue.ApplyV2Override(ctx, int64(created["id"].(float64)), cairn.V2Override{Field: "topics", Term: topic, Action: "accept", OperationKey: fmt.Sprintf("granularity-%d-%s", index, topic)})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	management := dashboard.New(ctx, health.NewTracker(), queue, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	defer management.Drain(time.Second)
	server := httptest.NewServer(management.Handler())
	defer server.Close()
	get := func(path string) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			return nil, err
		}
		return server.Client().Do(request)
	}
	for _, tc := range []struct {
		query string
		total int
	}{{"topics=image_creation,video_creation&topics_mode=any&topic_refinements=portrait_photography", 2}, {"topics=image_creation,video_creation&topics_mode=all&topic_refinements=portrait_photography", 0}, {"topic_refinements=portrait_photography,whiteboard_animation", 0}} {
		response, err := get("/api/bookmarks?limit=1&" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var page cairn.BookmarkPage
		decodeErr := json.NewDecoder(response.Body).Decode(&page)
		_ = response.Body.Close()
		if response.StatusCode != 200 || decodeErr != nil || page.Counts.Total != tc.total {
			t.Fatalf("real refined query: %s status=%d total=%d err=%v", tc.query, response.StatusCode, page.Counts.Total, decodeErr)
		}
	}
	if _, err := queue.ApplyV2Override(ctx, id, cairn.V2Override{Field: "topics", Term: "portrait_photography", Action: "reject", OperationKey: "human-reject-portrait"}); err != nil {
		t.Fatal(err)
	}
	response, err := get("/api/bookmarks?topics=image_creation&topic_refinements=portrait_photography")
	if err != nil {
		t.Fatal(err)
	}
	var after cairn.BookmarkPage
	decodeErr := json.NewDecoder(response.Body).Decode(&after)
	_ = response.Body.Close()
	if response.StatusCode != 200 || decodeErr != nil || after.Counts.Total != 0 {
		t.Fatal("human rejection did not invalidate the effective refinement")
	}
	t.Log("real Go/Worker/D1: production 31-question identity, 24-only incremental provider call, manifest v2 unknown partition, 11 GET smoke contracts, broad ANY/ALL + exact refinements, counts/paging and human rejection passed; no external models")
}
