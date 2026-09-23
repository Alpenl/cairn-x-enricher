package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// LiveOptions explicitly bounds one experiment. MaxTokens is the input-token
// reservation budget, including unsuccessful calls whose usage is unknown.
// Jev 1.13 documents a 64k input context per request; 65,536 is reserved before
// EACH attempt, without refunds. This is not a rune-to-token estimate.
type LiveOptions struct {
	Enabled    bool          `json:"enabled"`
	MaxSamples int           `json:"max_samples"`
	MaxCalls   int           `json:"max_calls"`
	MaxTokens  int64         `json:"max_input_tokens"`
	Timeout    time.Duration `json:"timeout"`
	Model      string        `json:"model"`
}

const jev113InputCeiling int64 = 65536
const modelLimitSource = "https://docs.typesafe.ai/models"

// LivePlan is the immutable pre-call budget and sample list.
type LivePlan struct {
	ReferenceHash           string      `json:"reference_hash"`
	SampleIDs               []string    `json:"sample_ids"`
	Calls                   int         `json:"calls"`
	InputReservation        int64       `json:"input_token_reservation"`
	PerCallInputReservation int64       `json:"per_call_input_reservation"`
	LimitSource             string      `json:"limit_source"`
	LimitCheckedAt          string      `json:"limit_checked_at"`
	SpecID                  string      `json:"spec_id"`
	SpecHash                string      `json:"spec_hash"`
	Model                   string      `json:"requested_model"`
	Options                 LiveOptions `json:"options"`
}

// LiveEvaluator is the actual production request/typed-judgment path.
type LiveEvaluator interface {
	BuildProviderRequest(classify.Input) ([]byte, classify.Evidence, error)
	Evaluate(context.Context, classify.Input) (classify.RawJudgments, error)
	Spec() classify.QuestionSpec
	Policy() classify.Policy
}

// CallRecord contains no auth headers. Its exact request and raw judgments are
// private experiment artifacts, written before/after each actual call.
type CallRecord struct {
	SampleID            string                 `json:"sample_id"`
	Phase               string                 `json:"phase"`
	ReferenceHash       string                 `json:"reference_hash"`
	SourceHash          string                 `json:"source_hash"`
	WireStateHash       string                 `json:"wire_state_hash"`
	Request             json.RawMessage        `json:"request,omitempty"`
	Raw                 *classify.RawJudgments `json:"raw,omitempty"`
	RequestedModel      string                 `json:"requested_model"`
	ResolvedModel       string                 `json:"resolved_model,omitempty"`
	SpecHash            string                 `json:"spec_hash"`
	Questions           int                    `json:"questions"`
	Attempt             int                    `json:"attempt"`
	Retry               bool                   `json:"retry"`
	Cached              bool                   `json:"cached"`
	LatencyMS           int64                  `json:"latency_ms"`
	ReservedInputTokens int64                  `json:"reserved_input_tokens"`
	InputTokens         int64                  `json:"input_tokens"`
	OutputTokens        int64                  `json:"output_tokens"`
	UsageKnown          bool                   `json:"usage_known"`
	Error               string                 `json:"error,omitempty"`
}

// LiveResult is returned even on failure, preserving completed predictions and
// honest reservations rather than losing already incurred costs.
type LiveResult struct {
	Dataset             Dataset  `json:"dataset"`
	Plan                LivePlan `json:"plan"`
	Calls               int      `json:"calls"`
	ReservedInputTokens int64    `json:"reserved_input_tokens"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	UsageMissingCalls   int      `json:"usage_missing_calls"`
	P50LatencyMS        int64    `json:"p50_latency_ms"`
	P95LatencyMS        int64    `json:"p95_latency_ms"`
	Completed           bool     `json:"completed"`
	StopReason          string   `json:"stop_reason,omitempty"`
}

// PlanLive checks the complete input and budgets before any network operation.
func PlanLive(dataset Dataset, evaluator LiveEvaluator, options LiveOptions) (LivePlan, error) {
	if err := dataset.Validate(); err != nil {
		return LivePlan{}, err
	}
	if len(dataset.Prediction) > 0 {
		return LivePlan{}, errors.New("live input must contain frozen references without predictions")
	}
	if options.Model != "jev-1.13.0" {
		return LivePlan{}, errors.New("live token ceiling is documented only for pinned jev-1.13.0; aliases and other versions require a new limit review")
	}
	if options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return LivePlan{}, errors.New("timeout must be positive and at most five minutes")
	}
	if len(dataset.Samples) == 0 || len(dataset.Samples) > options.MaxSamples || len(dataset.Samples) > options.MaxCalls || int64(len(dataset.Samples)) > options.MaxTokens/jev113InputCeiling {
		return LivePlan{}, errors.New("sample/call/input-token reservation budget is insufficient")
	}
	hash, err := HashReference(dataset)
	if err != nil {
		return LivePlan{}, err
	}
	spec := evaluator.Spec()
	plan := LivePlan{ReferenceHash: hash, Calls: len(dataset.Samples), InputReservation: int64(len(dataset.Samples)) * jev113InputCeiling, PerCallInputReservation: jev113InputCeiling, LimitSource: modelLimitSource, LimitCheckedAt: "2026-09-23", SpecID: spec.SpecID, SpecHash: spec.SemanticHash, Model: options.Model, Options: options}
	for _, sample := range dataset.Samples {
		if sample.Material == nil || sample.Gold == nil {
			return LivePlan{}, fmt.Errorf("sample %s lacks frozen material/reference", sample.SampleID)
		}
		body, _, err := evaluator.BuildProviderRequest(classify.Input{Evidence: sample.Material})
		if err != nil {
			return LivePlan{}, fmt.Errorf("sample %s preflight: %w", sample.SampleID, err)
		}
		var wire struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return LivePlan{}, err
		}
		if wire.Model != options.Model {
			return LivePlan{}, errors.New("evaluator model differs from frozen live plan")
		}
		plan.SampleIDs = append(plan.SampleIDs, sample.SampleID)
	}
	return plan, nil
}

// RunLive uses no retries, batching, cache or implicit policy fitting. Every
// attempt is durably reserved through record before Evaluate can access HTTP.
func RunLive(ctx context.Context, dataset Dataset, evaluator LiveEvaluator, options LiveOptions, record func(CallRecord) error) (result LiveResult, err error) {
	plan, err := PlanLive(dataset, evaluator, options)
	if err != nil {
		return result, err
	}
	result = LiveResult{Dataset: dataset, Plan: plan}
	if !options.Enabled {
		return result, errors.New("live evaluation requires explicit opt-in")
	}
	if record == nil {
		return result, errors.New("a durable private call recorder is required")
	}
	latencies := []int64{}
	defer func() {
		if err != nil {
			result.StopReason = err.Error()
		}
		if len(latencies) > 0 {
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			result.P50LatencyMS = latencies[(len(latencies)-1)/2]
			result.P95LatencyMS = latencies[(95*len(latencies)+99)/100-1]
		}
	}()
	for _, sample := range dataset.Samples {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		input := classify.Input{Evidence: sample.Material}
		body, _, buildErr := evaluator.BuildProviderRequest(input)
		if buildErr != nil {
			return result, buildErr
		}
		var wire struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return result, err
		}
		sum := sha256.Sum256(wire.State)
		entry := CallRecord{SampleID: sample.SampleID, Phase: "reserved", ReferenceHash: plan.ReferenceHash, SourceHash: sample.SourceHash, WireStateHash: hex.EncodeToString(sum[:]), Request: body,
			RequestedModel: options.Model, SpecHash: plan.SpecHash, Questions: len(wire.Questions), Attempt: 1, ReservedInputTokens: jev113InputCeiling}
		if err := record(entry); err != nil {
			return result, fmt.Errorf("reserve record: %w", err)
		}
		result.Calls++
		result.ReservedInputTokens += jev113InputCeiling
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		raw, callErr := evaluator.Evaluate(callCtx, input)
		cancel()
		entry.LatencyMS = time.Since(started).Milliseconds()
		latencies = append(latencies, entry.LatencyMS)
		entry.Phase = "finished"
		entry.Raw = &raw
		entry.ResolvedModel = raw.ResolvedModel
		var usage struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		}
		if json.Unmarshal(raw.Usage, &usage) == nil && !raw.UsageMissing && usage.Input != nil && usage.Output != nil && *usage.Input >= 0 && *usage.Output >= 0 {
			entry.UsageKnown = true
			entry.InputTokens = *usage.Input
			entry.OutputTokens = *usage.Output
			result.InputTokens += entry.InputTokens
			result.OutputTokens += entry.OutputTokens
		} else {
			result.UsageMissingCalls++
			entry.Error = "provider usage missing or invalid"
		}
		if callErr != nil {
			entry.Error = "provider evaluation failed; no automatic retry"
			if err := record(entry); err != nil {
				return result, fmt.Errorf("record failed call: %w", err)
			}
			return result, errors.New(entry.Error)
		}
		if raw.ResolvedModel != options.Model || raw.SpecHash != plan.SpecHash || raw.EvidenceHash != entry.WireStateHash || raw.Coverage != "complete" {
			entry.Error = "model/spec/answer coverage identity mismatch"
		}
		if entry.InputTokens > jev113InputCeiling {
			entry.Error = "provider exceeded the documented per-request input ceiling"
		}
		if err := record(entry); err != nil {
			return result, fmt.Errorf("record completed call: %w", err)
		}
		if entry.Error != "" {
			return result, errors.New(entry.Error)
		}
		prediction, err := PredictionFromJudgments(sample.SampleID, raw, evaluator.Policy())
		if err != nil {
			return result, errors.New("stored judgments could not be decided")
		}
		result.Dataset.Prediction = append(result.Dataset.Prediction, prediction)
	}
	result.Completed = true
	return result, nil
}

// PredictionFromJudgments uses the same pure production decision for live and
// offline wire recovery. It never performs inference or changes reference labels.
func PredictionFromJudgments(sampleID string, raw classify.RawJudgments, policy classify.Policy) (Prediction, error) {
	proposals, err := classify.Decide(raw, policy)
	if err != nil {
		return Prediction{}, err
	}
	return Prediction{SampleID: sampleID, SpecID: raw.SpecID, SpecHash: raw.SpecHash, Model: raw.ResolvedModel, PolicyVersion: policy.Version,
		Topics: proposals.Topics, ContentFunctions: proposals.ContentFunctions, Carriers: proposals.Carriers, Affordances: proposals.Affordances, Form: proposals.Form, Use: proposals.Use, TopicProbabilities: topicProbabilities(raw), Abstained: abstainedDimensions(proposals)}, nil
}
