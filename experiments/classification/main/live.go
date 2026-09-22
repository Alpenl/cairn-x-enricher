package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
	"github.com/joho/godotenv"
)

type liveConfig struct {
	catalogPath, output, envFile, model, baseURL, sampleID string
	maxSamples, maxCalls                                   int
	maxTokens                                              int64
	timeout                                                time.Duration
	dryRun                                                 bool
}

func runLiveCommand(dataset evaluation.Dataset, cfg liveConfig, gate evaluation.GateThresholds) error {
	catalogBytes, err := os.ReadFile(filepath.Clean(cfg.catalogPath))
	if err != nil {
		return errors.New("cannot read frozen catalog")
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		return err
	}
	sourceReferenceHash, err := evaluation.HashReference(dataset)
	if err != nil {
		return err
	}
	if cfg.sampleID != "" {
		selected := []evaluation.Sample{}
		for _, sample := range dataset.Samples {
			if sample.SampleID == cfg.sampleID {
				selected = append(selected, sample)
			}
		}
		if len(selected) != 1 {
			return errors.New("sample-id must identify exactly one frozen sample")
		}
		dataset.Samples = selected
	}
	endpoint, err := url.Parse(cfg.baseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" {
		return errors.New("live base URL must be HTTPS without embedded credentials or query")
	}
	key := "dry-run-no-credential"
	if !cfg.dryRun {
		key = os.Getenv("TYPESAFE_API_KEY")
		if cfg.envFile != "" {
			values, err := godotenv.Read(filepath.Clean(cfg.envFile))
			if err != nil {
				return errors.New("cannot read explicit credential file")
			}
			key = values["TYPESAFE_API_KEY"]
		}
		if key == "" {
			return errors.New("TYPESAFE_API_KEY is not configured")
		}
	}
	httpClient := &http.Client{Timeout: cfg.timeout}
	client, err := classify.NewClient(cfg.baseURL, key, cfg.model, httpClient, catalog)
	if err != nil {
		return err
	}
	options := evaluation.LiveOptions{Enabled: !cfg.dryRun, Model: cfg.model, MaxSamples: cfg.maxSamples, MaxCalls: cfg.maxCalls, MaxTokens: cfg.maxTokens, Timeout: cfg.timeout}
	plan, err := evaluation.PlanLive(dataset, client, options)
	if err != nil {
		return err
	}
	if cfg.dryRun {
		encode(map[string]any{"plan": plan, "source_reference_hash": sourceReferenceHash, "model_calls": 0})
		return nil
	}
	if cfg.output == "" {
		return errors.New("live requires a new private -output directory")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.output), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(cfg.output, 0o700); err != nil {
		return errors.New("output directory must be new; inspect existing records instead of repeating paid calls")
	}
	if err := privateJSON(cfg.output, "plan.json", map[string]any{"plan": plan, "source_reference_hash": sourceReferenceHash, "gate": gate, "catalog": catalog}); err != nil {
		return err
	}
	journal, err := os.OpenFile(filepath.Join(cfg.output, "calls.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	wire, err := os.OpenFile(filepath.Join(cfg.output, "wire.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = wire.Close() }()
	httpClient.Transport = &wireRecorder{base: http.DefaultTransport, file: wire}
	// NewClient copies http.Client; install the recorder before constructing the live instance.
	client, err = classify.NewClient(cfg.baseURL, key, cfg.model, httpClient, catalog)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	recorder := func(entry evaluation.CallRecord) error {
		if err := json.NewEncoder(journal).Encode(entry); err != nil {
			return err
		}
		if err := journal.Sync(); err != nil {
			return err
		}
		if entry.Phase == "finished" {
			fmt.Fprintf(os.Stderr, "sample=%s phase=%s usage_known=%t input_tokens=%d elapsed_ms=%d\n", entry.SampleID, entry.Phase, entry.UsageKnown, entry.InputTokens, entry.LatencyMS)
		}
		return nil
	}
	result, runErr := evaluation.RunLive(ctx, dataset, client, options, recorder)
	if err := privateJSON(cfg.output, "result.json", result); err != nil {
		return err
	}
	report, scoreErr := evaluation.Score(result.Dataset)
	if scoreErr == nil {
		if err := privateJSON(cfg.output, "report.json", map[string]any{"report": report, "gate": gate, "decision": evaluation.EvaluateGate(report, gate)}); err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	if scoreErr != nil {
		return scoreErr
	}
	encode(map[string]any{"completed": result.Completed, "calls": result.Calls, "input_tokens": result.InputTokens, "reserved_input_tokens": result.ReservedInputTokens, "usage_missing_calls": result.UsageMissingCalls, "reference_hash": plan.ReferenceHash, "artifacts": cfg.output})
	return nil
}
func privateJSON(root, name string, value any) error {
	file, err := os.OpenFile(filepath.Clean(filepath.Join(root, name)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return file.Sync()
}

type wireRecorder struct {
	base http.RoundTripper
	file *os.File
	mu   sync.Mutex
}

func (w *wireRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	// Authentication headers and endpoint URLs are deliberately not serialized.
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(body))
	response, err := w.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	w.mu.Lock()
	defer w.mu.Unlock()
	entry := map[string]any{"request": json.RawMessage(body), "response_status": response.StatusCode, "response_bytes": len(raw), "response": string(raw)}
	if err := json.NewEncoder(w.file).Encode(entry); err != nil {
		return nil, err
	}
	if err := w.file.Sync(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	return response, nil
}
