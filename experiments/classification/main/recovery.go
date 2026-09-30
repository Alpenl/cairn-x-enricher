package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type savedWire struct {
	Request       json.RawMessage `json:"request"`
	Response      string          `json:"response"`
	ResponseBytes int             `json:"response_bytes"`
	Status        int             `json:"response_status"`
}

type savedPlan struct {
	Plan                evaluation.LivePlan `json:"plan"`
	SourceReferenceHash string              `json:"source_reference_hash"`
	Catalog             taxonomy.Catalog    `json:"catalog"`
}

type recoveredCall struct {
	SampleID        string                 `json:"sample_id"`
	SourceDirectory string                 `json:"source_directory"`
	OriginalError   string                 `json:"original_error,omitempty"`
	RecoveryError   string                 `json:"recovery_error,omitempty"`
	InputTokens     int64                  `json:"input_tokens"`
	OutputTokens    int64                  `json:"output_tokens"`
	UsageKnown      bool                   `json:"usage_known"`
	LatencyMS       int64                  `json:"latency_ms"`
	Raw             *classify.RawJudgments `json:"raw,omitempty"`
}

type recoveryResult struct {
	ReferenceHash        string            `json:"reference_hash"`
	ModelCalls           int               `json:"new_model_calls"`
	OriginalCalls        int               `json:"original_calls"`
	OriginalReservations int64             `json:"original_reserved_input_tokens"`
	InputTokens          int64             `json:"reported_input_tokens"`
	OutputTokens         int64             `json:"reported_output_tokens"`
	UsageMissingCalls    int               `json:"usage_missing_calls"`
	ArtifactHashes       map[string]string `json:"source_artifact_hashes"`
	Calls                []recoveredCall   `json:"calls"`
	Recovered            int               `json:"recovered_predictions"`
	Unattempted          int               `json:"unattempted_samples"`
}

// savedTransport has no network implementation or fallback. The production
// decoder receives exactly one recorded response for an identical request.
type savedTransport struct {
	wire savedWire
	used bool
}

func (s *savedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	if s.used || !bytes.Equal(body, s.wire.Request) {
		return nil, errors.New("saved request mismatch or repeated offline read")
	}
	s.used = true
	return &http.Response{StatusCode: s.wire.Status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(s.wire.Response)), Request: req}, nil
}

func artifact(root, name string, hashes map[string]string) ([]byte, error) {
	path := filepath.Clean(filepath.Join(root, name))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	hashes[path] = hex.EncodeToString(sum[:])
	return data, nil
}
func decodeLines[T any](data []byte) ([]T, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var values []T
	for scanner.Scan() {
		var value T
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, scanner.Err()
}

func recoverWires(dataset evaluation.Dataset, directories []string) (evaluation.Dataset, evaluation.Dataset, recoveryResult, error) {
	result := recoveryResult{ArtifactHashes: map[string]string{}}
	fail := func(err error) (evaluation.Dataset, evaluation.Dataset, recoveryResult, error) {
		return evaluation.Dataset{}, evaluation.Dataset{}, result, err
	}
	if err := dataset.Validate(); err != nil {
		return fail(err)
	}
	if len(dataset.Prediction) > 0 {
		return fail(errors.New("recovery requires frozen references without predictions"))
	}
	var err error
	result.ReferenceHash, err = evaluation.HashReference(dataset)
	if err != nil {
		return fail(err)
	}
	samples := map[string]evaluation.Sample{}
	for _, sample := range dataset.Samples {
		samples[sample.SampleID] = sample
	}
	attempted := map[string]bool{}
	predictions := map[string]evaluation.Prediction{}
	model, specHash := "", ""
	for _, root := range directories {
		var plan savedPlan
		data, err := artifact(root, "plan.json", result.ArtifactHashes)
		if err != nil {
			return fail(err)
		}
		if err := json.Unmarshal(data, &plan); err != nil {
			return fail(err)
		}
		subset := dataset
		subset.Samples = nil
		planned := map[string]bool{}
		for _, id := range plan.Plan.SampleIDs {
			sample, ok := samples[id]
			if !ok || planned[id] {
				return fail(errors.New("saved plan contains missing or duplicate sample"))
			}
			planned[id] = true
			subset.Samples = append(subset.Samples, sample)
		}
		hash, err := evaluation.HashReference(subset)
		if err != nil {
			return fail(err)
		}
		// A continuation may have a subset source hash. Its complete sample material
		// and reference labels must still hash identically within the parent corpus.
		if hash != plan.Plan.ReferenceHash || (plan.SourceReferenceHash != hash && plan.SourceReferenceHash != result.ReferenceHash) {
			return fail(errors.New("saved reference identity mismatch"))
		}
		if model != "" && (model != plan.Plan.Model || specHash != plan.Plan.SpecHash) {
			return fail(errors.New("cannot merge different model/spec identities"))
		}
		model, specHash = plan.Plan.Model, plan.Plan.SpecHash
		data, err = artifact(root, "calls.ndjson", result.ArtifactHashes)
		if err != nil {
			return fail(err)
		}
		records, err := decodeLines[evaluation.CallRecord](data)
		if err != nil {
			return fail(err)
		}
		data, err = artifact(root, "wire.ndjson", result.ArtifactHashes)
		if err != nil {
			return fail(err)
		}
		wires, err := decodeLines[savedWire](data)
		if err != nil {
			return fail(err)
		}
		// The live runner is sequential and writes reserved/finished pairs. An
		// interrupted or ambiguous journal needs inspection, never an implicit retry.
		if len(records)%2 != 0 || len(records)/2 != len(wires) {
			return fail(errors.New("incomplete or ambiguous call/wire journal; no automatic retry"))
		}
		if len(wires) > len(plan.Plan.SampleIDs) {
			return fail(errors.New("call count exceeds saved plan"))
		}
		for index, wire := range wires {
			reserved, finished := records[2*index], records[2*index+1]
			id := reserved.SampleID
			sample := samples[id]
			if !planned[id] || id != plan.Plan.SampleIDs[index] || attempted[id] || reserved.Phase != "reserved" || finished.Phase != "finished" || finished.SampleID != id {
				return fail(errors.New("call order, phase, or duplicate sample mismatch"))
			}
			if reserved.ReferenceHash != hash || finished.ReferenceHash != hash || reserved.SourceHash != sample.SourceHash || finished.SourceHash != sample.SourceHash || reserved.SpecHash != specHash || finished.SpecHash != specHash || reserved.RequestedModel != model || finished.RequestedModel != model {
				return fail(errors.New("call reference/source/model/spec mismatch"))
			}
			if reserved.Attempt != 1 || finished.Attempt != 1 || reserved.Retry || finished.Retry || reserved.Cached || finished.Cached || reserved.ReservedInputTokens != plan.Plan.PerCallInputReservation || finished.ReservedInputTokens != reserved.ReservedInputTokens {
				return fail(errors.New("unsupported retry/cache/reservation identity"))
			}
			if !bytes.Equal(reserved.Request, finished.Request) || !bytes.Equal(reserved.Request, wire.Request) || len(wire.Response) != wire.ResponseBytes {
				return fail(errors.New("saved wire bytes differ from call journal"))
			}
			transport := &savedTransport{wire: wire}
			client, err := classify.NewClient("https://offline.invalid", "offline-no-credential", model, &http.Client{Transport: transport}, plan.Catalog)
			if err != nil {
				return fail(err)
			}
			if client.Spec().SemanticHash != specHash || client.SpecID() != plan.Plan.SpecID {
				return fail(errors.New("compiled spec differs; old wire cannot answer changed questions"))
			}
			input := classify.Input{Evidence: sample.Material}
			request, _, err := client.BuildProviderRequest(input)
			if err != nil {
				return fail(err)
			}
			if !bytes.Equal(request, wire.Request) {
				return fail(errors.New("material/questions differ from exact saved provider request"))
			}
			var state struct {
				State     json.RawMessage            `json:"state"`
				Questions map[string]json.RawMessage `json:"questions"`
			}
			if err := json.Unmarshal(request, &state); err != nil {
				return fail(err)
			}
			stateSum := sha256.Sum256(state.State)
			if hex.EncodeToString(stateSum[:]) != reserved.WireStateHash || finished.WireStateHash != reserved.WireStateHash || reserved.Questions != len(state.Questions) || finished.Questions != reserved.Questions {
				return fail(errors.New("wire state or question identity mismatch"))
			}
			attempted[id] = true
			result.OriginalCalls++
			result.OriginalReservations += reserved.ReservedInputTokens
			call := recoveredCall{SampleID: id, SourceDirectory: root, OriginalError: finished.Error, LatencyMS: finished.LatencyMS}
			// Usage is provider telemetry, independent of whether an answer validates.
			var envelope struct {
				Usage struct {
					Input  *int64 `json:"input_tokens"`
					Output *int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(wire.Response), &envelope) == nil && envelope.Usage.Input != nil && envelope.Usage.Output != nil && *envelope.Usage.Input >= 0 && *envelope.Usage.Output >= 0 {
				call.UsageKnown = true
				call.InputTokens = *envelope.Usage.Input
				call.OutputTokens = *envelope.Usage.Output
				result.InputTokens += call.InputTokens
				result.OutputTokens += call.OutputTokens
			} else {
				result.UsageMissingCalls++
			}
			raw, decodeErr := client.Evaluate(context.Background(), input)
			// Offline decoding is not a new inference; retain original provider latency.
			for i := range raw.Calls {
				raw.Calls[i].LatencyMS = finished.LatencyMS
			}
			switch {
			case decodeErr != nil:
				call.RecoveryError = "saved response rejected by production decoder"
			case !call.UsageKnown:
				call.RecoveryError = "usage missing or invalid"
			case call.InputTokens > reserved.ReservedInputTokens:
				call.RecoveryError = "provider exceeded reserved input ceiling"
			case raw.ResolvedModel != model || raw.SpecHash != specHash || raw.EvidenceHash != reserved.WireStateHash || raw.Coverage != "complete":
				call.RecoveryError = "resolved model/spec/evidence/coverage mismatch"
			default:
				prediction, err := evaluation.PredictionFromJudgments(id, raw, client.Policy())
				if err != nil {
					return fail(err)
				}
				predictions[id] = prediction
				call.Raw = &raw
				result.Recovered++
			}
			result.Calls = append(result.Calls, call)
		}
	}
	recovered := dataset
	unattempted := dataset
	unattempted.Samples = nil
	for _, sample := range dataset.Samples {
		if prediction, ok := predictions[sample.SampleID]; ok {
			recovered.Prediction = append(recovered.Prediction, prediction)
		}
		if !attempted[sample.SampleID] {
			unattempted.Samples = append(unattempted.Samples, sample)
		}
	}
	result.Unattempted = len(unattempted.Samples)
	sort.Slice(result.Calls, func(i, j int) bool { return result.Calls[i].SampleID < result.Calls[j].SampleID })
	return recovered, unattempted, result, nil
}

func runRecoveryCommand(dataset evaluation.Dataset, roots, output string, dryRun bool, gate evaluation.GateThresholds) error {
	if err := gate.Validate(); err != nil {
		return fmt.Errorf("invalid gate: %w", err)
	}
	recovered, pending, result, err := recoverWires(dataset, strings.Split(roots, ","))
	if err != nil {
		return err
	}
	summary := map[string]any{"new_model_calls": 0, "original_calls": result.OriginalCalls, "recovered_predictions": result.Recovered, "unattempted_samples": result.Unattempted, "reported_input_tokens": result.InputTokens, "usage_missing_calls": result.UsageMissingCalls, "reference_hash": result.ReferenceHash}
	if dryRun {
		encode(summary)
		return nil
	}
	if output == "" {
		return errors.New("recovery needs a new private -output directory")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("recovery output must be new: %w", err)
	}
	for _, file := range []struct {
		name  string
		value any
	}{{"recovery.json", result}, {"dataset.json", recovered}, {"unattempted.json", pending}} {
		if err := privateJSON(output, file.name, file.value); err != nil {
			return err
		}
	}
	report, err := evaluation.Score(recovered)
	if err != nil {
		return err
	}
	if err := privateJSON(output, "report.json", map[string]any{"report": report, "gate": gate, "decision": evaluation.EvaluateGate(report, gate)}); err != nil {
		return err
	}
	summary["artifacts"] = output
	encode(summary)
	return nil
}
