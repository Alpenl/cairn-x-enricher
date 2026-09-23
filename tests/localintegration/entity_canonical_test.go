package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type canonicalObservation struct {
	Candidate struct {
		BlockID string `json:"block_id"`
	} `json:"candidate"`
	CanonicalID    string `json:"canonical_id"`
	CatalogVersion string `json:"catalog_version"`
}

func TestLocalWorkerCanonicalEntitiesCLI(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, token, http.DefaultClient)
	catalog, err := queue.GetV2Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var entityCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Questions map[string]struct {
				Type     string                     `json:"type"`
				Criteria map[string]json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := payload.Questions["entity_0"]; ok {
			entityCalls.Add(1)
		}
		answers := map[string]any{}
		for id, q := range payload.Questions {
			if q.Type == "noul" {
				answers[id] = map[string]any{"type": "noul", "noul": .9}
				continue
			}
			options := []string{}
			for option := range q.Criteria {
				options = append(options, option)
			}
			sort.Strings(options)
			if len(options) == 0 {
				t.Error("no options")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			selected := options[0]
			if strings.HasPrefix(id, "entity_") {
				selected = "relevant"
			}
			if strings.HasPrefix(id, "canonical_") {
				selected = "unknown"
				for _, option := range options {
					if strings.HasPrefix(option, "id:") {
						selected = option
						break
					}
				}
			}
			probabilities := map[string]float64{}
			for _, option := range options {
				probabilities[option] = 0
			}
			probabilities[selected] = 1
			answers[id] = map[string]any{"type": "choice", "choice": selected, "probabilities": probabilities, "confidence": 1}
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
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	job, err := queue.ClaimByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim %v", err)
	}
	source := enrich.Source{OriginalText: "Acme https://example.com/a", ContextText: "Acme", OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "fixture"}
	if err := queue.SaveSource(ctx, id, job.LeaseToken, source); err != nil {
		t.Fatal(err)
	}
	snapshot := processor.EvidenceSnapshot(source, time.Now())
	snapshot["blocks"] = append(snapshot["blocks"].([]map[string]any), map[string]any{"id": "external-b", "role": "external_article", "text": "Acme", "url": "https://example.com/b", "acquired": "controlled_fetch", "relation": "synthetic identity fixture"})
	if err := queue.SubmitEvidence(ctx, id, snapshot); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	binary := filepath.Join(work, "cairn-x-enricher")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cairn-x-enricher") // #nosec G204 -- fixed package and isolated test output, no shell or remote arguments.
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	catalogPath := filepath.Join(work, "identities.json")
	writeCatalog := func(version string) {
		t.Helper()
		raw := `{"version":"` + version + `","entities":[{"id":"acme-a","label":"Acme","kind":"project","aliases":[],"identifiers":["https://example.com/a"]},{"id":"acme-b","label":"Acme","kind":"organization","aliases":[],"identifiers":["https://example.com/b"]}]}`
		if err := os.WriteFile(catalogPath, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog("fixture-1")
	run := func(retry bool) {
		t.Helper()
		command := exec.CommandContext(ctx, "./cairn-x-enricher", "classify", "--max-jobs", "1")
		if retry {
			command.Args = append(command.Args, "--id", strconv.FormatInt(id, 10))
		}
		command.Dir = work
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "CAIRN_API_BASE_URL=" + base, "CAIRN_ENRICHER_TOKEN=" + token, "TYPESAFE_BASE_URL=" + provider.URL, "TYPESAFE_API_KEY=fixture", "TYPESAFE_MODEL=jev-1.13.0", "CAIRN_EXTENSION_ENTITIES=true", "CAIRN_ENTITY_CATALOG_PATH=" + catalogPath, "LOG_LEVEL=error"}
		var out, stderr bytes.Buffer
		command.Stdout = &out
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("CLI %v %s", err, stderr.String())
		}
		var result struct {
			Classified int `json:"classified"`
			Failed     int `json:"failed"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Classified != 1 || result.Failed != 0 {
			t.Fatalf("CLI result %s %v", out.String(), err)
		}
	}
	read := func() []canonicalObservation {
		t.Helper()
		raw, err := queue.GetEntities(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			State        string                 `json:"state"`
			Observations []canonicalObservation `json:"observations"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.State != "completed_nonempty" || len(result.Observations) != 3 {
			t.Fatalf("stored identity %s %v", raw, err)
		}
		return result.Observations
	}
	run(false)
	first := read()
	identities := map[string]string{}
	for _, o := range first {
		identities[o.Candidate.BlockID] = o.CanonicalID
		if o.CatalogVersion != "fixture-1" {
			t.Fatalf("missing catalog provenance: %+v", o)
		}
	}
	if identities["primary-1"] != "acme-a" || identities["external-b"] != "acme-b" || identities["context-1"] != "" {
		t.Fatalf("same-name identity merge: %v", identities)
	}
	run(true)
	if entityCalls.Load() != 1 {
		t.Fatalf("restart repeated entity request: %d", entityCalls.Load())
	}
	read()
	writeCatalog("fixture-2")
	run(true)
	updated := read()
	if entityCalls.Load() != 2 || updated[0].CatalogVersion != "fixture-2" {
		t.Fatalf("catalog change reused stale judgment: calls=%d observations=%+v", entityCalls.Load(), updated)
	}
	t.Log("three real CLI processes load the controlled catalog; two equal names keep different IDs, unsupported occurrence stays unknown; restart reuses raw Choice results; catalog version change invalidates cache; two local entity HTTP calls, zero paid")
}
