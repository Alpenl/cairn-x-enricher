package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

func TestLocalWorkerEntityCacheCLI(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, token, http.DefaultClient)
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var entityCalls, classificationCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			w.WriteHeader(400)
			return
		}
		_, entity := payload.Questions["entity_0"]
		if entity {
			entityCalls.Add(1)
		} else {
			classificationCalls.Add(1)
		}
		answers := map[string]any{}
		for id, q := range payload.Questions {
			if q.Type == "noul" {
				answers[id] = map[string]any{"type": "noul", "noul": .9}
				continue
			}
			var options map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &options) != nil {
				w.WriteHeader(400)
				return
			}
			prob := map[string]float64{}
			pick := ""
			for option := range options {
				prob[option] = 0
				if pick == "" {
					pick = option
				}
			}
			if entity {
				pick = "relevant"
			}
			prob[pick] = 1
			answers[id] = map[string]any{"type": "choice", "choice": pick, "probabilities": prob, "confidence": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}})
	}))
	defer provider.Close()
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-1.13.0", provider.Client(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier, "jev-1.13.0")
	created := postJSON(ctx, t, base+"/api/links", envOr("CAIRN_APP_TOKEN", "app"), map[string]any{"url": "https://x.com/entitycache/status/98001"})
	id := int64(created["id"].(float64))
	sourceJob, err := queue.Claim(ctx)
	if err != nil || sourceJob == nil || sourceJob.ID != id {
		t.Fatalf("source claim: %+v %v", sourceJob, err)
	}
	source := enrich.Source{OriginalText: "ExampleEntity", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "source-fixture"}
	if err := queue.SaveSource(ctx, id, sourceJob.LeaseToken, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	var lostCache, lostState atomic.Bool
	var statePosts, stateFailures atomic.Int32
	var keysMu sync.Mutex
	keys := map[string]bool{}
	// Forward to the real Worker; discard only a response after its write committed.
	target, err := url.Parse(base)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.User != nil {
		t.Fatal("fixture requires a loopback Worker")
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	forward.ModifyResponse = func(response *http.Response) error {
		path := response.Request.URL.Path
		if strings.HasSuffix(path, "/entity-state") {
			statePosts.Add(1)
			if response.StatusCode != http.StatusOK {
				stateFailures.Add(1)
			}
		}
		if response.StatusCode == http.StatusOK && strings.Contains(path, "/entity-cache/") && strings.HasSuffix(path, "/complete") {
			keysMu.Lock()
			keys[strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2/entity-cache/"), "/complete")] = true
			keysMu.Unlock()
			if lostCache.CompareAndSwap(false, true) {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				return io.ErrUnexpectedEOF
			}
		}
		if response.StatusCode == http.StatusOK && strings.HasSuffix(path, "/entity-state") && lostState.CompareAndSwap(false, true) {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	forward.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "committed fixture response lost", http.StatusBadGateway)
	}
	proxy := httptest.NewServer(forward)
	defer proxy.Close()
	work := t.TempDir()
	binary := filepath.Join(work, "cairn-x-enricher")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cairn-x-enricher") // #nosec G204 -- fixed package, isolated test output path, no shell.
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	run := func(retry bool, limit int) {
		t.Helper()
		command := exec.CommandContext(ctx, "./cairn-x-enricher", "classify", "--max-jobs", "1")
		if retry {
			command.Args = append(command.Args, "--id", strconv.FormatInt(id, 10))
		}
		command.Dir = work
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "CAIRN_API_BASE_URL=" + proxy.URL, "CAIRN_ENRICHER_TOKEN=" + token, "TYPESAFE_BASE_URL=" + provider.URL, "TYPESAFE_API_KEY=fixture", "TYPESAFE_MODEL=jev-1.13.0", "CAIRN_EXTENSION_ENTITIES=true", "CAIRN_EXTENSION_MAX_CALLS=" + strconv.Itoa(limit), "LOG_LEVEL=error"}
		var out, stderr bytes.Buffer
		command.Stdout = &out
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("CLI: %v %s", err, stderr.String())
		}
		var result struct {
			Classified int `json:"classified"`
			Failed     int `json:"failed"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Classified != 1 || result.Failed != 0 {
			t.Fatalf("result %s %v", out.String(), err)
		}
	}
	readEntities := func() map[string]any {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/links/%d/entities", base, id), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var value map[string]any
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil || response.StatusCode != 200 {
			t.Fatalf("entities %v %d", err, response.StatusCode)
		}
		return value
	}
	run(false, 2)
	if entityCalls.Load() != 1 || readEntities()["state"] != "completed_nonempty" {
		t.Fatalf("initial entity calls=%d state=%v", entityCalls.Load(), readEntities())
	}
	run(true, 2)
	if entityCalls.Load() != 1 {
		t.Fatalf("unchanged material charged again across CLI restart: entity HTTP=%d want1", entityCalls.Load())
	}
	if !lostCache.Load() || !lostState.Load() {
		t.Fatal("lost-response recovery was not exercised")
	}
	postJSON(ctx, t, fmt.Sprintf("%s/api/v2/links/%d/entities", base, id), token, map[string]any{"operation_key": "entity-human-reject", "action": "reject", "term": "ExampleEntity"})
	run(true, 1)
	if entityCalls.Load() != 1 || len(readEntities()["entities"].([]any)) != 0 {
		t.Fatalf("exhausted-budget hit lost human correction: %v", readEntities())
	}
	source.OriginalText = "OtherEntity"
	if err := queue.SaveSource(ctx, id, sourceJob.LeaseToken, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	run(true, 2)
	if entityCalls.Load() != 2 {
		t.Fatalf("changed material not evaluated: %d", entityCalls.Load())
	}
	entityView := readEntities()
	if values := entityView["entities"].([]any); len(values) != 1 || values[0] != "OtherEntity" {
		t.Fatalf("new entity state %v", entityView)
	}
	source.OriginalText = "ThirdEntity"
	if err := queue.SaveSource(ctx, id, sourceJob.LeaseToken, source); err != nil {
		t.Fatal(err)
	}
	if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatal(err)
	}
	run(true, 2)
	if entityCalls.Load() != 2 || readEntities()["state"] != "failed" {
		t.Fatalf("new source exhausted budget inferred or hid failure: %v", readEntities())
	}
	if statePosts.Load() != 6 || stateFailures.Load() != 0 {
		t.Fatalf("entity commits posts=%d failures=%d", statePosts.Load(), stateFailures.Load())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/links/%d", base, id), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+envOr("CAIRN_APP_TOKEN", "app"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete %d", response.StatusCode)
	}
	keysMu.Lock()
	savedKeys := make([]string, 0, len(keys))
	for key := range keys {
		savedKeys = append(savedKeys, key)
	}
	keysMu.Unlock()
	if len(savedKeys) != 3 {
		t.Fatalf("expected three source identities, got%d", len(savedKeys))
	}
	for _, key := range savedKeys {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/entity-cache/"+key, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("deleted private cache remained %d", res.StatusCode)
		}
	}
	t.Logf("PASS: five real CLI processes; %d ordinary classification HTTP / %d entity HTTP to local fixture; lost cache and state acknowledgments recovered; restart/human override/exhausted-budget reuse; source invalidation and explicit budget failure; deletion removes all private results; zero paid calls", classificationCalls.Load(), entityCalls.Load())
}
