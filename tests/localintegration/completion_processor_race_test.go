package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type completionRaceState struct {
	Status         string  `json:"status"`
	Classification *string `json:"classification"`
	Projection     *string `json:"projection"`
	Runs           int     `json:"runs"`
	Decisions      int     `json:"decisions"`
	Operations     int     `json:"operations"`
}

func completionRaceCatalog() taxonomy.Catalog {
	return taxonomy.Catalog{Version: "2026-09-20.1",
		Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Description: "Language models", Active: true}},
		Forms:  []taxonomy.Term{{ID: "method", Label: "Method", Description: "Method", Active: true}},
		Uses:   []taxonomy.Term{{ID: "try", Label: "Try", Description: "Try", Active: true}}}
}

func readCompletionRaceState(ctx context.Context, t *testing.T, base, token string, id int64) completionRaceState {
	t.Helper()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/__test__/completion-race/%d", base, id), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read local D1 race state: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read local D1 race state status = %d", response.StatusCode)
	}
	var state completionRaceState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode local D1 race state: %v", err)
	}
	return state
}

// A successful control proves the enabled entity extension would run. The two
// fault cases use the same full Processor and real Go HTTP client, then move
// D1 identity after the Worker's last preflight and before its commit batch.
func TestLocalWorkerProcessorCompletionPreflightRace(t *testing.T) {
	base := workerURL(t)
	change := os.Getenv("CAIRN_RACE_CHANGE")
	if change != "success" && change != "content" && change != "target" && change != "cancelcontent" {
		t.Fatal("CAIRN_RACE_CHANGE must be success, content, target or cancelcontent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	appToken := envOr("CAIRN_APP_TOKEN", "app")
	enricherToken := envOr("CAIRN_ENRICHER_TOKEN", "internal")
	catalog := completionRaceCatalog()
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var modelCalls atomic.Int64
	providerHTTP := provider.Client()
	providerTransport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		modelCalls.Add(1)
		return providerTransport.RoundTrip(request)
	})
	classifier, err := classify.NewClient(provider.URL, "local-key", "jev-latest", providerHTTP, catalog)
	if err != nil {
		t.Fatal(err)
	}
	setupQueue := cairn.NewClient(base, enricherToken, &http.Client{Timeout: 30 * time.Second})
	id := createLink(t, base, appToken)
	lease := claimEnrichmentJob(t, base, enricherToken, id)
	source := enrich.Source{OriginalText: "ExternalEntity appears in a practical LLM evaluation guide.",
		OriginalLanguage: "en", RelatedLinks: []string{}, ImageURLs: []string{}, Model: "manual"}
	if err := setupQueue.SaveSource(ctx, id, lease, source); err != nil {
		t.Fatalf("save source: %v", err)
	}
	if err := setupQueue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
		t.Fatalf("submit evidence: %v", err)
	}
	if err := setupQueue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatalf("register question spec: %v", err)
	}
	switchTarget(t, base, enricherToken, classifier)
	baseline := readCompletionRaceState(ctx, t, base, enricherToken, id)
	if baseline.Status != "pending" || baseline.Runs != 0 || baseline.Decisions != 0 || baseline.Operations != 0 {
		t.Fatalf("unexpected pre-claim D1 state: %+v", baseline)
	}

	var completionCalls, entityWrites int
	var barrierBatches string
	var completionPayload []byte
	raceHTTP := &http.Client{Timeout: 30 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/complete") {
			completionCalls++
			payload, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				return nil, readErr
			}
			_ = request.Body.Close()
			completionPayload = append([]byte(nil), payload...)
			request.Body = io.NopCloser(bytes.NewReader(payload))
			if change != "success" && completionCalls == 1 {
				if change == "cancelcontent" {
					// The processor's already-leased work context stays bounded
					// but independent of the scheduling context being cancelled.
					stopRun()
					request.Header.Set("X-Cairn-Test-Completion-Race", "content")
				} else {
					request.Header.Set("X-Cairn-Test-Completion-Race", change)
				}
			}
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/entity-state") {
			entityWrites++
		}
		response, err := http.DefaultTransport.RoundTrip(request)
		if err == nil && request.Header.Get("X-Cairn-Test-Completion-Race") != "" {
			barrierBatches = response.Header.Get("X-Cairn-Test-Barrier-Batches")
		}
		return response, err
	})}
	raceQueue := cairn.NewClient(base, enricherToken, raceHTTP)
	if err := raceQueue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatalf("register processor question spec: %v", err)
	}
	p := processor.NewStaged(raceQueue, nil, classifier, catalog.Version, "jev-latest",
		slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	entities := extension.NewService(extension.Flags{Entities: true}, extension.DefaultBudget(), classifier)
	entities.SetBudgetStore(raceQueue)
	p.SetExtensions(entities, nil, extension.FetchPolicy{})
	done, failed, runErr := p.RunClassifications(runCtx, 1)
	state := readCompletionRaceState(ctx, t, base, enricherToken, id)
	if change == "success" {
		if runErr != nil || done != 1 || failed != 0 || completionCalls != 1 ||
			entityWrites == 0 || state.Status != "completed" || state.Runs != 1 ||
			state.Decisions != 1 || state.Operations != 1 {
			t.Fatalf("successful processor control: done=%d failed=%d err=%v completion=%d entity=%d state=%+v",
				done, failed, runErr, completionCalls, entityWrites, state)
		}
		return
	}
	var apiErr *cairn.APIError
	if done != 0 || failed != 1 || !errors.As(runErr, &apiErr) || apiErr.StatusCode != http.StatusConflict ||
		apiErr.Code == "already_completed" || completionCalls != 1 || barrierBatches != "1" ||
		modelCalls.Load() != 1 || entityWrites != 0 || len(completionPayload) == 0 {
		t.Fatalf("%s processor race: done=%d failed=%d err=%v completion=%d barrier=%q model=%d entity=%d",
			change, done, failed, runErr, completionCalls, barrierBatches, modelCalls.Load(), entityWrites)
	}
	if state.Status != "processing" || state.Classification != nil || state.Runs != 0 ||
		state.Decisions != 0 || state.Operations != 0 ||
		(state.Projection == nil) != (baseline.Projection == nil) ||
		(state.Projection != nil && *state.Projection != *baseline.Projection) {
		t.Fatalf("%s processor race left success state: before=%+v after=%+v", change, baseline, state)
	}
	// Replay the exact serialized request captured from the processor. A
	// spurious completion receipt would turn this into HTTP 200.
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/enrichment/classifications/%d/complete", base, id), bytes.NewReader(completionPayload))
	request.Header.Set("Authorization", "Bearer "+enricherToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Cairn-Classification-Budget", "1")
	replay, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("replay processor completion: %v", err)
	}
	defer func() { _ = replay.Body.Close() }()
	var rejection struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(replay.Body).Decode(&rejection); err != nil ||
		replay.StatusCode != http.StatusConflict || rejection.Error == "" || rejection.Error == "already_completed" {
		t.Fatalf("processor replay status=%d body=%+v err=%v", replay.StatusCode, rejection, err)
	}
	if replayedState := readCompletionRaceState(ctx, t, base, enricherToken, id); !reflect.DeepEqual(replayedState, state) {
		t.Fatalf("processor replay changed D1 state: before=%+v after=%+v", state, replayedState)
	}
	if modelCalls.Load() != 1 || entityWrites != 0 {
		t.Fatalf("replay performed paid or extension work: model=%d entity=%d", modelCalls.Load(), entityWrites)
	}
}
