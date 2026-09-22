package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

var policyDimensions = []string{"topics", "content_functions", "carriers", "affordances", "form", "use"}

// ReplayBinding is the exact inference identity on which a policy was fitted.
// A changed model, question meaning, taxonomy or batching requires new evidence.
type ReplayBinding struct {
	Model           string `json:"resolved_model"`
	SpecID          string `json:"spec_id"`
	SpecHash        string `json:"spec_hash"`
	TaxonomyVersion string `json:"taxonomy_version"`
	BatchSemantics  string `json:"batch_semantics"`
}

type noInferenceTransport struct{}

func (noInferenceTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("policy replay has no network transport")
}

// VerifiedReplay holds validated records without exposing mutable caller data.
// Construction checks the full input; candidates then reuse that validation.
type VerifiedReplay struct {
	dataset Dataset
	binding ReplayBinding
}

// PreparePolicyReplay binds every stored answer to the sample's actual bounded
// production state and compiled spec. Neither constructor nor Replay calls a
// model; even an accidental request has a transport that cannot use the network.
func PreparePolicyReplay(dataset Dataset, catalog taxonomy.Catalog, model string) (*VerifiedReplay, error) {
	if err := dataset.Validate(); err != nil {
		return nil, err
	}
	if len(dataset.Samples) == 0 || len(dataset.Prediction) != len(dataset.Samples) {
		return nil, errors.New("policy replay requires one saved evaluation for every sample")
	}
	// Deep copy once: caller mutation cannot invalidate the checked identities.
	encoded, err := json.Marshal(dataset)
	if err != nil {
		return nil, err
	}
	var frozen Dataset
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return nil, err
	}
	client, err := classify.NewClient("https://offline.invalid", "offline-no-credential", model,
		&http.Client{Transport: noInferenceTransport{}}, catalog)
	if err != nil {
		return nil, err
	}
	byID := map[string]Prediction{}
	for _, p := range frozen.Prediction {
		byID[p.SampleID] = p
	}
	verified := &VerifiedReplay{dataset: frozen}
	for _, sample := range frozen.Samples {
		p := byID[sample.SampleID]
		raw := p.Evaluation
		if sample.Material == nil || raw == nil {
			return nil, fmt.Errorf("sample %s lacks source material or raw judgments", sample.SampleID)
		}
		if model == "" || raw.RequestedModel != model || raw.ResolvedModel != model || raw.AliasDrift ||
			p.Model != model || p.SpecID != raw.SpecID || p.SpecHash != raw.SpecHash {
			return nil, fmt.Errorf("sample %s model/spec identity mismatch", sample.SampleID)
		}
		if err := classify.ValidateReplayMetadata(client.Spec(), *raw); err != nil {
			return nil, fmt.Errorf("sample %s: %w", sample.SampleID, err)
		}
		body, _, err := client.BuildProviderRequest(classify.Input{Evidence: sample.Material})
		if err != nil {
			return nil, err
		}
		var request struct {
			State json.RawMessage `json:"state"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		if !bytes.Equal(request.State, []byte(raw.WireState)) || digest(request.State) != raw.EvidenceHash {
			return nil, fmt.Errorf("sample %s material changed; new inference is required", sample.SampleID)
		}
		binding := ReplayBinding{Model: model, SpecID: raw.SpecID, SpecHash: raw.SpecHash,
			TaxonomyVersion: raw.TaxonomyVersion, BatchSemantics: raw.BatchSemantics}
		if verified.binding == (ReplayBinding{}) {
			verified.binding = binding
		}
		if verified.binding != binding {
			return nil, errors.New("cannot mix inference identities in one replay")
		}
	}
	return verified, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Replay applies all six production dimensions without changing reference labels.
func (v *VerifiedReplay) Replay(policy classify.Policy) (Dataset, error) {
	if v == nil {
		return Dataset{}, errors.New("missing verified replay")
	}
	if err := policy.Validate(); err != nil {
		return Dataset{}, err
	}
	result := v.dataset
	result.Prediction = make([]Prediction, 0, len(v.dataset.Prediction))
	for _, old := range v.dataset.Prediction {
		p, err := PredictionFromJudgments(old.SampleID, *old.Evaluation, policy)
		if err != nil {
			return Dataset{}, err
		}
		result.Prediction = append(result.Prediction, p)
	}
	// Return a deep copy of reference material so consumers cannot mutate the
	// checked instance through a previous replay result.
	encoded, err := json.Marshal(result)
	if err != nil {
		return Dataset{}, err
	}
	var detached Dataset
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return Dataset{}, err
	}
	return detached, nil
}

// FitRisk declares the objective before fitting. False-negative loss includes
// missed positives under abstention; review loss adds the cost of handling it.
// Rates use known-reference samples per dimension, never number of labels.
type FitRisk struct {
	FalsePositive    float64            `json:"false_positive"`
	FalseNegative    float64            `json:"false_negative"`
	Review           float64            `json:"review"`
	DimensionWeights map[string]float64 `json:"dimension_weights"`
}

// PolicyFitConfig freezes search space, loss, coverage constraints and scope.
// Thresholds are shared across the three Noul and three Choice dimensions,
// matching production and avoiding unsupported per-rare-label tuning.
type PolicyFitConfig struct {
	Version          string             `json:"version"`
	Scope            string             `json:"scope"`
	Base             classify.Policy    `json:"base_policy"`
	Accept           []float64          `json:"noul_accept_candidates"`
	Reject           []float64          `json:"noul_reject_candidates"`
	Choice           []float64          `json:"choice_accept_candidates"`
	MinCoverage      map[string]float64 `json:"min_dimension_coverage"`
	MaxAcceptedError float64            `json:"max_accepted_error"`
	Risk             FitRisk            `json:"risk"`
}

// Validate checks the finite search space, six-dimensional scope and loss rules.
func (c PolicyFitConfig) Validate() error {
	if c.Version == "" || c.Scope == "" {
		return errors.New("fit version and sample scope are required")
	}
	if err := c.Base.Validate(); err != nil {
		return err
	}
	if c.Base.Calibrated || c.Base.AllowAliasDrift {
		return errors.New("fitting cannot claim calibration or allow model drift")
	}
	if len(c.Accept) == 0 || len(c.Reject) == 0 || len(c.Choice) == 0 || len(c.Accept) > 32 || len(c.Reject) > 32 || len(c.Choice) > 32 || len(c.Accept)*len(c.Reject)*len(c.Choice) > 1024 {
		return errors.New("fit requires between 1 and 1024 bounded threshold combinations")
	}
	for _, values := range [][]float64{c.Accept, c.Reject, c.Choice} {
		seen := map[float64]bool{}
		for _, value := range values {
			if !unitInterval(value) || seen[value] {
				return errors.New("candidate bounds must be unique finite probabilities")
			}
			seen[value] = true
		}
	}
	if len(c.MinCoverage) != 6 || len(c.Risk.DimensionWeights) != 6 || !unitInterval(c.MaxAcceptedError) {
		return errors.New("fit requires six explicit coverage floors and risk weights")
	}
	for _, name := range policyDimensions {
		floor, ok := c.MinCoverage[name]
		weight := c.Risk.DimensionWeights[name]
		if !ok || !unitInterval(floor) || math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			return errors.New("invalid dimension risk or coverage")
		}
	}
	for _, cost := range []float64{c.Risk.FalsePositive, c.Risk.FalseNegative, c.Risk.Review} {
		if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			return errors.New("loss weights must be finite and nonnegative")
		}
	}
	if c.Risk.FalsePositive == 0 || c.Risk.FalseNegative == 0 {
		return errors.New("positive false-positive and false-negative costs are required")
	}
	return nil
}

// FitCandidate records a searched policy and why it met or failed constraints.
type FitCandidate struct {
	Policy            classify.Policy    `json:"policy"`
	Loss              float64            `json:"loss"`
	Eligible          bool               `json:"eligible"`
	Reasons           []string           `json:"reasons,omitempty"`
	Coverage          float64            `json:"coverage"`
	AcceptedError     float64            `json:"accepted_error"`
	DimensionCoverage map[string]float64 `json:"dimension_coverage"`
}

// PolicyFitArtifact is a reproducible candidate, never a production promotion.
// The report carries reference provenance and the candidate remains uncalibrated.
type PolicyFitArtifact struct {
	Version           string           `json:"version"`
	Config            PolicyFitConfig  `json:"config"`
	ConfigHash        string           `json:"config_hash"`
	Binding           ReplayBinding    `json:"binding"`
	DatasetHash       string           `json:"dataset_hash"`
	ReferenceHash     string           `json:"reference_hash"`
	Split             string           `json:"split"`
	Samples           int              `json:"samples"`
	IndependentGroups int              `json:"independent_groups"`
	ModelCalls        int              `json:"model_calls"`
	Promote           bool             `json:"promote"`
	Candidates        []FitCandidate   `json:"candidates"`
	Selected          *classify.Policy `json:"selected_policy,omitempty"`
	SelectedReport    *Report          `json:"selected_report,omitempty"`
	BaselineReport    Report           `json:"baseline_report"`
	Status            string           `json:"status"`
}

// ReplayFittedPolicy checks the exported policy's binding before using it on
// another verified dataset. It does not establish holdout access authorization,
// independent validation, calibration, or production promotion.
func ReplayFittedPolicy(v *VerifiedReplay, artifact PolicyFitArtifact) (Dataset, error) {
	if v == nil || artifact.Version != "cairn-policy-fit-v1" || artifact.Status != "fitted_unvalidated" || artifact.Selected == nil || artifact.Promote || artifact.ModelCalls != 0 || artifact.Binding != v.binding {
		return Dataset{}, errors.New("fitted policy is missing, promoted or bound to another inference identity")
	}
	if artifact.Split != "train" && artifact.Split != "dev" {
		return Dataset{}, errors.New("fitted policy has an invalid source split")
	}
	if err := artifact.Config.Validate(); err != nil {
		return Dataset{}, err
	}
	encoded, err := json.Marshal(artifact.Config)
	if err != nil {
		return Dataset{}, err
	}
	if digest(encoded) != artifact.ConfigHash || !validDigest(artifact.ReferenceHash) || !validDigest(artifact.DatasetHash) {
		return Dataset{}, errors.New("fitted policy configuration or reference identity changed")
	}
	p := *artifact.Selected
	expected := artifact.Config.Base
	expected.TopicAccept = p.TopicAccept
	expected.TopicReject = p.TopicReject
	expected.ChoiceAccept = p.ChoiceAccept
	identity, err := json.Marshal(struct {
		Binding    ReplayBinding
		ConfigHash string
		Policy     classify.Policy
	}{artifact.Binding, artifact.ConfigHash, expected})
	if err != nil {
		return Dataset{}, err
	}
	expected.Version = "fit-" + digest(identity)[:24]
	if p != expected || p.TopicReject >= p.TopicAccept || !slices.Contains(artifact.Config.Accept, p.TopicAccept) || !slices.Contains(artifact.Config.Reject, p.TopicReject) || !slices.Contains(artifact.Config.Choice, p.ChoiceAccept) {
		return Dataset{}, errors.New("selected policy differs from its frozen candidate identity")
	}
	eligible := false
	for _, candidate := range artifact.Candidates {
		if candidate.Policy == p && candidate.Eligible {
			eligible = true
		}
	}
	if !eligible {
		return Dataset{}, errors.New("selected policy has no eligible fitted candidate")
	}
	return v.Replay(p)
}

// FitProductionPolicy searches all six dimensions through production Decide.
// The verified dataset must be train or dev; holdout is never fitted.
func FitProductionPolicy(v *VerifiedReplay, config PolicyFitConfig) (PolicyFitArtifact, error) {
	if v == nil || (v.dataset.Split != "train" && v.dataset.Split != "dev") {
		return PolicyFitArtifact{}, errors.New("policy fitting requires train or dev, never holdout")
	}
	if err := config.Validate(); err != nil {
		return PolicyFitArtifact{}, err
	}
	if err := ValidateSplits([]Dataset{v.dataset}); err != nil {
		return PolicyFitArtifact{}, err
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return PolicyFitArtifact{}, err
	}
	ref, err := HashReference(v.dataset)
	if err != nil {
		return PolicyFitArtifact{}, err
	}
	dataHash, err := HashDataset(v.dataset)
	if err != nil {
		return PolicyFitArtifact{}, err
	}
	artifact := PolicyFitArtifact{Version: "cairn-policy-fit-v1", Config: config, ConfigHash: digest(configBytes), Binding: v.binding, DatasetHash: dataHash, ReferenceHash: ref, Split: v.dataset.Split, Samples: len(v.dataset.Samples), Status: "no_eligible_candidate"}
	baseline, err := v.Replay(config.Base)
	if err != nil {
		return PolicyFitArtifact{}, err
	}
	artifact.BaselineReport, err = Score(baseline)
	if err != nil {
		return PolicyFitArtifact{}, err
	}
	// Sorting makes ties conservative and independent of config list order:
	// lower false acceptance thresholds lose ties to higher accept thresholds;
	// rejection uses the lower (more abstaining) bound on an exact tie.
	accept, reject, choice := append([]float64{}, config.Accept...), append([]float64{}, config.Reject...), append([]float64{}, config.Choice...)
	sort.Sort(sort.Reverse(sort.Float64Slice(accept)))
	sort.Float64s(reject)
	sort.Sort(sort.Reverse(sort.Float64Slice(choice)))
	best := math.Inf(1)
	for _, a := range accept {
		for _, r := range reject {
			for _, c := range choice {
				if r >= a {
					continue
				}
				p := config.Base
				p.TopicAccept = a
				p.TopicReject = r
				p.ChoiceAccept = c
				identity, err := json.Marshal(struct {
					Binding    ReplayBinding
					ConfigHash string
					Policy     classify.Policy
				}{v.binding, artifact.ConfigHash, p})
				if err != nil {
					return PolicyFitArtifact{}, err
				}
				p.Version = "fit-" + digest(identity)[:24]
				dataset, err := v.Replay(p)
				if err != nil {
					return PolicyFitArtifact{}, err
				}
				report, err := Score(dataset)
				if err != nil {
					return PolicyFitArtifact{}, err
				}
				artifact.IndependentGroups = report.IndependentGroups
				candidate := fitCandidate(p, report, config)
				if math.IsNaN(candidate.Loss) || math.IsInf(candidate.Loss, 0) {
					return PolicyFitArtifact{}, errors.New("nonfinite fit loss")
				}
				artifact.Candidates = append(artifact.Candidates, candidate)
				if candidate.Eligible && candidate.Loss < best-1e-12 {
					best = candidate.Loss
					artifact.Selected = &p
					artifact.SelectedReport = &report
					artifact.Status = "fitted_unvalidated"
				}
			}
		}
	}
	if len(artifact.Candidates) == 0 {
		return PolicyFitArtifact{}, errors.New("no candidate has reject strictly below accept")
	}
	return artifact, nil
}

func fitCandidate(policy classify.Policy, report Report, config PolicyFitConfig) FitCandidate {
	c := FitCandidate{Policy: policy, Eligible: true, Coverage: report.Coverage, AcceptedError: report.AcceptedError, DimensionCoverage: map[string]float64{}}
	weightSum := 0.0
	for _, metric := range report.Dimensions {
		weight := config.Risk.DimensionWeights[metric.Dimension]
		weightSum += weight
		coverage := ratio(metric.Decided, metric.Support)
		c.DimensionCoverage[metric.Dimension] = coverage
		if metric.Support == 0 || coverage < config.MinCoverage[metric.Dimension] {
			c.Reasons = append(c.Reasons, metric.Dimension+": missing support or insufficient coverage")
		}
		if metric.Support > 0 {
			c.Loss += weight * (config.Risk.FalsePositive*float64(metric.FalsePos) + config.Risk.FalseNegative*float64(metric.FalseNeg) + config.Risk.Review*float64(metric.Abstained)) / float64(metric.Support)
		}
	}
	if weightSum > 0 {
		c.Loss /= weightSum
	}
	if report.SamplesMissing > 0 || report.MissingGoldCount > 0 {
		c.Reasons = append(c.Reasons, "missing references or predictions")
	}
	if report.AcceptedError > config.MaxAcceptedError {
		c.Reasons = append(c.Reasons, "accepted error exceeds frozen limit")
	}
	c.Eligible = len(c.Reasons) == 0
	return c
}
