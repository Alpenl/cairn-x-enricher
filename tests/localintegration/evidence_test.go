package localintegration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// This fixture replaces only retrieval/reading model boundaries. All source,
// snapshot, lease and completion operations use the real HTTP client and D1.
type checkpointReader struct{ fetches int }

func (r *checkpointReader) FetchSource(context.Context, enrich.Input) (enrich.Source, error) {
	r.fetches++
	return enrich.Source{OriginalText: "Synthetic <LLM> & evaluation 中文\u2028source " + strings.Repeat("长材料", 5000), OriginalLanguage: "en", Model: "fixture", RelatedLinks: []string{}, ImageURLs: []string{}}, nil
}

func (*checkpointReader) Transform(_ context.Context, input enrich.Input) (enrich.Result, error) {
	return enrich.Result{OriginalText: input.SourceText, OriginalLanguage: "en", AITitle: "Synthetic title", TranslatedText: "合成评估材料", Summary: "Synthetic reading aid", Model: "fixture"}, nil
}

func TestLocalWorkerEvidenceCheckpointAndBoundRead(t *testing.T) {
	base := workerURL(t)
	token := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	catalog := taxonomy.Catalog{Version: "2026-09-20.1",
		Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language models", Active: true}},
		Forms:  []taxonomy.Term{{ID: "method", Label: "Method", Description: "Method", Active: true}},
		Uses:   []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try", Active: true}}}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var calls atomic.Int64
	budget := classify.Budget{MaxRunes: 200, MaxBlocks: 2, MaxStateBytes: 1000, MaxRequestBytes: 16 << 10}
	var requestBytes, stateBytes, stateRunes, stateBlocks int
	var truncated bool
	providerHTTP := provider.Client()
	providerTransport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		var request struct {
			State json.RawMessage `json:"state"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		var evidence classify.Evidence
		if err := json.Unmarshal(request.State, &evidence); err != nil {
			return nil, err
		}
		requestBytes = len(body)
		stateBytes = len(request.State)
		stateRunes = utf8.RuneCountInString(evidence.Primary)
		stateBlocks = len(evidence.Context)
		for _, block := range evidence.Context {
			stateRunes += utf8.RuneCountInString(block.Text)
		}
		truncated = evidence.Truncated && evidence.Coverage == "truncated"
		return providerTransport.RoundTrip(r)
	})
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", providerHTTP, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := classifier.SetBudget(budget); err != nil {
		t.Fatal(err)
	}
	queue := cairn.NewClient(base, token, &http.Client{Timeout: 10 * time.Second})
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, token, classifier)
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))

	var snapshotPosts int
	faultHTTP := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/evidence") {
			snapshotPosts++
			if snapshotPosts == 1 {
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"transient_error"}`)), Request: r}, nil
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	faultQueue := cairn.NewClient(base, token, faultHTTP)
	reader := &checkpointReader{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := processor.NewStaged(faultQueue, reader, classifier, catalog.Version, "jev-latest", logger, 1)
	job, err := queue.ClaimByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim source: %v", err)
	}
	if err := p.Process(ctx, job); err == nil {
		t.Fatal("expected first snapshot write to fail")
	}
	stored, err := queue.GetSource(ctx, id)
	if err != nil || stored == nil {
		t.Fatalf("source checkpoint lost: %v", err)
	}
	for range 3 {
		pending, err := queue.ClaimClassification(ctx, classifier.SpecID(), catalog.Version, "jev-latest")
		if err != nil || pending != nil {
			t.Fatalf("source without snapshot was claimed: %+v %v", pending, err)
		}
	}
	status, err := queue.GetClassificationStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var waiting struct {
		Attempts int    `json:"attempts"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(status, &waiting); err != nil {
		t.Fatal(err)
	}
	if waiting.Attempts != 0 || waiting.Status != "pending" {
		t.Fatalf("waiting burned an attempt: %s", status)
	}

	job, err = queue.ClaimByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("reclaim source: %v", err)
	}
	if err := p.Process(ctx, job); err != nil {
		t.Fatalf("checkpoint repair: %v", err)
	}
	if reader.fetches != 1 || snapshotPosts != 2 {
		t.Fatalf("fetches=%d snapshot posts=%d", reader.fetches, snapshotPosts)
	}
	if calls.Load() != 0 {
		t.Fatal("classification ran before checkpoint repair")
	}
	t.Log("source saved, snapshot 503, three empty claims without attempt consumption, retry repairs snapshot with one retrieval")
	archivedBefore, err := queue.GetEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	for _, fault := range []string{"404", "503", "malformed", "id", "revision", "hash", "body"} {
		t.Run(fault, func(t *testing.T) {
			var leased *cairn.ClassificationJob
			httpClient := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response, err := http.DefaultTransport.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/classifications/claim") && response.StatusCode == 200 {
					body, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if readErr != nil {
						return nil, readErr
					}
					if err := json.Unmarshal(body, &leased); err != nil {
						return nil, err
					}
					response.Body = io.NopCloser(strings.NewReader(string(body)))
				}
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/evidence") || r.URL.Query().Get("snapshot_id") == "" {
					return response, nil
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil {
					return nil, err
				}
				var view map[string]json.RawMessage
				if err := json.Unmarshal(body, &view); err != nil {
					return nil, err
				}
				switch fault {
				case "404":
					response.StatusCode = 404
					body = []byte(`{"error":"not_found"}`)
				case "503":
					response.StatusCode = 503
					body = []byte(`{"error":"transient_error"}`)
				case "malformed":
					body = []byte(`{"snapshot":`)
				case "id":
					view["id"] = json.RawMessage(`999999`)
				case "revision":
					view["content_revision"] = json.RawMessage(`999999`)
				case "hash":
					view["content_hash"] = json.RawMessage(`"wrong"`)
				case "body":
					view["snapshot"] = json.RawMessage(strings.Replace(string(view["snapshot"]), "Synthetic", "Altered", 1))
				}
				if fault != "404" && fault != "503" && fault != "malformed" {
					body, err = json.Marshal(view)
					if err != nil {
						return nil, err
					}
				}
				response.Body = io.NopCloser(strings.NewReader(string(body)))
				response.ContentLength = int64(len(body))
				return response, nil
			})}
			q := cairn.NewClient(base, token, httpClient)
			attempt := processor.NewStaged(q, nil, classifier, catalog.Version, "jev-latest", logger, 1)
			done, _, _ := attempt.RunClassifications(ctx, 1)
			if leased == nil || done != 0 || calls.Load() != 0 {
				t.Fatalf("invalid evidence reached model: lease=%v done=%d calls=%d", leased != nil, done, calls.Load())
			}
			runs, err := queue.GetRuns(ctx, id)
			if err != nil || len(runs) != 0 {
				t.Fatalf("invalid evidence created a run: %d %v", len(runs), err)
			}
			// End this intentionally damaged attempt, then retry the actual
			// Worker job for the next injected transport failure.
			_ = queue.FailClassification(ctx, leased, "synthetic transport failure")
			if err := queue.RetryClassification(ctx, id); err != nil {
				t.Fatal(err)
			}
		})
	}
	healthy := processor.NewStaged(queue, nil, classifier, catalog.Version, "jev-latest", logger, 1)
	done, failed, err := healthy.RunClassifications(ctx, 1)
	if err != nil || done != 1 || failed != 0 || calls.Load() != 1 {
		t.Fatalf("recovery: done=%d failed=%d calls=%d err=%v", done, failed, calls.Load(), err)
	}
	runs, err := queue.GetRuns(ctx, id)
	if err != nil || len(runs) != 1 {
		t.Fatalf("successful runs=%d err=%v", len(runs), err)
	}
	if requestBytes > budget.MaxRequestBytes || stateBytes > budget.MaxStateBytes || stateRunes > budget.MaxRunes || stateBlocks > budget.MaxBlocks || !truncated {
		t.Fatalf("real bound snapshot bypassed budget: request=%d state=%d runes=%d blocks=%d truncated=%v", requestBytes, stateBytes, stateRunes, stateBlocks, truncated)
	}
	archivedAfter, err := queue.GetEvidence(ctx, id)
	if err != nil || string(archivedBefore) != string(archivedAfter) {
		t.Fatalf("archived snapshot changed during bounded inference: %v", err)
	}
	t.Logf("real bound snapshot retained %d archive bytes; outbound request=%d state=%d runes=%d blocks=%d, truncation recorded", len(archivedAfter), requestBytes, stateBytes, stateRunes, stateBlocks)
	t.Logf("seven bound-read failures: zero model calls/commits; recovery: %d model call, %d run", calls.Load(), len(runs))
}
