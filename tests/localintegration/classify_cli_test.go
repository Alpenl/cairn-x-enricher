package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestLocalWorkerClassifyCLI(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec := mustSpec(t, catalog)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected external operation %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		var request struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Questions) != len(spec.Questions) {
			t.Errorf("actual CLI omitted multidimensional questions: got%d want%d", len(request.Questions), len(spec.Questions))
			w.WriteHeader(422)
			return
		}
		answers := map[string]any{}
		for _, q := range spec.Questions {
			if _, ok := request.Questions[q.ID]; !ok {
				t.Errorf("actual CLI missing %s", q.ID)
				w.WriteHeader(422)
				return
			}
			if q.Kind == classify.QuestionNoul {
				p := .01
				if q.ID == "topic_llm" || q.ID == "content_functions_method" || q.ID == "affordances_practice" {
					p = .99
				}
				answers[q.ID] = map[string]any{"type": "noul", "noul": p}
				continue
			}
			wanted := map[string]string{"form": "method", "use": "try", "carriers": "single"}[q.ID]
			probabilities := map[string]float64{}
			for _, option := range q.AnswerOptions() {
				probabilities[option] = 0
			}
			probabilities[wanted] = .99
			probabilities["none"] = .01
			answers[q.ID] = map[string]any{"type": "choice", "choice": wanted, "probabilities": probabilities, "confidence": .9}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 123, "output_tokens": 10}})
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-1.13.0", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, spec); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier, "jev-1.13.0")
	var ids []int64
	sources := map[int64]enrich.Source{}
	for i := 1; i <= 2; i++ {
		created := postJSON(ctx, t, base+"/api/links", envOr("CAIRN_APP_TOKEN", "app"), map[string]any{"url": fmt.Sprintf("https://x.com/cli/status/%d", i), "note": "synthetic private note must not be model evidence"})
		id := int64(created["id"].(float64))
		ids = append(ids, id)
		lease := claimEnrichmentJob(t, base, token, id)
		source := enrich.Source{OriginalText: fmt.Sprintf("Saved language model evaluation method %d.", i), OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "source-fixture"}
		sources[id] = source
		if err := queue.SaveSource(ctx, id, lease, source); err != nil {
			t.Fatal(err)
		}
		if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	revision := int64(0)
	if _, err := queue.ApplyV2Override(ctx, ids[0], cairn.V2Override{OperationKey: "cli-human", Field: "use", Action: "accept", Term: "contra", ExpectedRevision: &revision}); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	binary := filepath.Join(work, "cairn-x-enricher")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cairn-x-enricher") // #nosec G204 -- fixed Go package; output is an isolated t.TempDir path, with no user input or shell.
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	execute := func(want, retryID int64) {
		t.Helper()
		command := exec.CommandContext(ctx, "./cairn-x-enricher", "classify", "--max-jobs", "1")
		if retryID > 0 {
			command.Args = append(command.Args, "--id", strconv.FormatInt(retryID, 10))
		}
		command.Dir = work // main loads .env; this directory contains only the test binary.
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "CAIRN_API_BASE_URL=" + base, "CAIRN_ENRICHER_TOKEN=" + token, "TYPESAFE_BASE_URL=" + provider.URL, "TYPESAFE_API_KEY=fixture", "TYPESAFE_MODEL=jev-1.13.0", "LOG_LEVEL=error"}
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("actual CLI: %v %s", err, stderr.String())
		}
		var result struct {
			Classified int64 `json:"classified"`
			Failed     int64 `json:"failed"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Classified != want || result.Failed != 0 {
			t.Fatalf("CLI result %s error=%v", stdout.String(), err)
		}
	}
	execute(1, 0)
	count := 0
	for _, id := range ids {
		run, err := queue.GetLatestRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil {
			count++
		}
	}
	if count != 1 || calls.Load() != 1 {
		t.Fatalf("--max-jobs 1 exceeded: stored=%d calls=%d", count, calls.Load())
	}
	execute(1, 0)
	execute(0, 0)
	if calls.Load() != 2 {
		t.Fatalf("empty drain repeated model: %d", calls.Load())
	}
	execute(1, ids[0])
	if calls.Load() != 3 {
		t.Fatalf("explicit retry call count=%d", calls.Load())
	}
	for _, id := range ids {
		run, err := queue.GetLatestRun(ctx, id)
		if err != nil || run == nil || run.SpecID != spec.SpecID || run.SpecHash != spec.SemanticHash || run.PolicyVersion != classify.PolicyVersion {
			t.Fatalf("stored CLI identity: %+v %v", run, err)
		}
		raw, err := run.DecodeJudgments(spec)
		if err != nil || len(raw.Judgments) != len(spec.Questions) {
			t.Fatalf("actual saved raw incomplete: %v", err)
		}
		decision, err := queue.GetLatestDecision(ctx, id)
		if err != nil || decision == nil {
			t.Fatal(err)
		}
		var automatic classify.AutomaticView
		if err := json.Unmarshal(decision.Automatic, &automatic); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(automatic.ContentFunctions, []string{"method"}) || !reflect.DeepEqual(automatic.Carriers, []string{"single"}) || !reflect.DeepEqual(automatic.Affordances, []string{"practice"}) {
			t.Fatalf("CLI lost multidimensional values: %+v", automatic)
		}
		saved, err := queue.GetSource(ctx, id)
		if err != nil || saved == nil || !reflect.DeepEqual(*saved, sources[id]) {
			t.Fatalf("classification changed saved source: %v", err)
		}
	}
	effective, err := queue.GetV2Effective(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Effective struct {
			Use string `json:"use"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(effective, &view); err != nil || view.Effective.Use != "contra" {
		t.Fatalf("CLI overwrote explicit human value: %v", err)
	}
	t.Log("actual compiled classify CLI: same service v2 spec; two bounded one-job drains, empty drain zero calls, explicit --id retry; full dimensional raw/decisions persisted; saved source and human choice unchanged; 3 local model fixture calls, no source retrieval")
}
