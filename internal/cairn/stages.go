package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

// ClassificationTarget is the Worker's authoritative classification goal. The
// consumer declares capabilities; it never defines the target itself.
type ClassificationTarget struct {
	Generation      int64  `json:"generation"`
	SpecID          string `json:"spec_id"`
	SpecHash        string `json:"spec_hash"`
	TaxonomyVersion string `json:"taxonomy_version"`
	PolicyVersion   string `json:"policy_version"`
	RequestedModel  string `json:"requested_model"`
	Protocol        string `json:"protocol"`
}

// Capabilities is what a consumer supports. It is sent with the handshake so
// the Worker can reject an incompatible consumer as a component condition
// instead of draining the queue into repeated per-job failures.
type Capabilities struct {
	Protocol         string   `json:"protocol"`
	SpecIDs          []string `json:"spec_ids"`
	TaxonomyVersions []string `json:"taxonomy_versions"`
	PolicyVersions   []string `json:"policy_versions"`
	Models           []string `json:"models"`
}

// HandshakeResult reports the active target and whether this consumer supports it.
type HandshakeResult struct {
	Target    ClassificationTarget `json:"target"`
	Supported bool                 `json:"supported"`
}

// Handshake queries the authoritative target and declares local capabilities.
// It is read-only: it can never change the server target.
func (c *Client) Handshake(ctx context.Context, caps Capabilities) (HandshakeResult, error) {
	values := url.Values{}
	values.Set("protocol", caps.Protocol)
	if len(caps.SpecIDs) > 0 {
		values.Set("spec_ids", strings.Join(caps.SpecIDs, ","))
	}
	if len(caps.TaxonomyVersions) > 0 {
		values.Set("taxonomy_versions", strings.Join(caps.TaxonomyVersions, ","))
	}
	if len(caps.PolicyVersions) > 0 {
		values.Set("policy_versions", strings.Join(caps.PolicyVersions, ","))
	}
	if len(caps.Models) > 0 {
		values.Set("models", strings.Join(caps.Models, ","))
	}
	response, err := c.do(ctx, http.MethodGet, "/api/enrichment/classifications/target?"+values.Encode(), nil)
	if err != nil {
		return HandshakeResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return HandshakeResult{}, apiError(response)
	}
	var result HandshakeResult
	if err := decodeJSON(response.Body, &result); err != nil {
		return HandshakeResult{}, fmt.Errorf("decode handshake: %w", err)
	}
	if result.Target.SpecID == "" || result.Target.Generation < 0 {
		return HandshakeResult{}, errors.New("handshake response is missing the target")
	}
	return result, nil
}

// ClassificationJob identifies a leased, versioned snapshot for semantic work.
type ClassificationJob struct {
	ID               int64  `json:"id"`
	Revision         int64  `json:"revision"`
	InputRevision    int64  `json:"input_revision"`
	TargetGeneration int64  `json:"target_generation"`
	SpecID           string `json:"spec_id"`
	Attempt          int    `json:"attempt"`
	LeaseToken       string `json:"lease_token"`
	LeaseUntil       string `json:"lease_until"`
	// RelatedLinks are the stored source links, used only by the opt-in
	// evidence escalation to find a real material gap.
	RelatedLinks []string `json:"related_links,omitempty"`
	// ContentRevision, EvidenceSnapshotID and EvidenceHash are the identity the
	// lease was bound to; the completion echoes them back so the Worker can
	// prove the inference and the stored run describe the same input (R2-02).
	ContentRevision    int64  `json:"content_revision,omitempty"`
	EvidenceSnapshotID int64  `json:"evidence_snapshot_id,omitempty"`
	EvidenceHash       string `json:"evidence_hash,omitempty"`
	// BoundSnapshot is populated only after the lease identity and canonical
	// payload have been verified. Extensions consume these exact archived blocks.
	BoundSnapshot json.RawMessage `json:"-"`
	classify.Input
}

// GetSource returns a snapshot only when it still matches the saved bookmark.
func (c *Client) GetSource(ctx context.Context, id int64) (*enrich.Source, error) {
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/enrichment/jobs/%d/source", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var source enrich.Source
	if err := decodeJSON(response.Body, &source); err != nil {
		return nil, err
	}
	return &source, nil
}

// SaveSource checkpoints retrieval and atomically queues classification.
func (c *Client) SaveSource(ctx context.Context, id int64, token string, source enrich.Source) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/jobs/%d/source", id), map[string]any{"lease_token": token, "source": source})
}

// ClaimClassification acquires semantic work independently of retrieval leases.
// It first negotiates the authoritative target so the lease is bound to the
// server's goal rather than to the local policy version. A capability mismatch
// is returned as a classified configuration error so the caller pauses the
// component instead of burning the queue's attempts.
func (c *Client) ClaimClassification(ctx context.Context, specID, version, model string) (*ClassificationJob, error) {
	handshake, err := c.Handshake(ctx, ClassificationCapabilities(specID, version, model))
	if err != nil {
		return nil, err
	}
	if !handshake.Supported {
		return nil, enrich.Classified(fmt.Errorf("classification target %q generation %d is not supported by this consumer", handshake.Target.SpecID, handshake.Target.Generation), enrich.ErrorClassConfiguration)
	}
	response, err := c.do(ctx, http.MethodPost, "/api/enrichment/classifications/claim", map[string]any{
		"protocol":          "v2",
		"spec_ids":          []string{handshake.Target.SpecID},
		"taxonomy_versions": []string{handshake.Target.TaxonomyVersion},
		"policy_versions":   []string{handshake.Target.PolicyVersion},
		"models":            []string{handshake.Target.RequestedModel},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var job ClassificationJob
	if err := decodeJSON(response.Body, &job); err != nil {
		return nil, err
	}
	if job.ID < 1 || job.Revision < 1 || job.LeaseToken == "" || job.OriginalText == "" {
		return nil, fmt.Errorf("invalid classification job")
	}
	return &job, nil
}

// ClassificationCapabilities describes the single target this build supports.
// Keeping it in one place means the handshake, the claim and a retry can never
// disagree about what this consumer can process. The spec id comes from the
// compiled question set so enabling Score or redefining a question changes the
// announced identity instead of silently reusing the old one.
func ClassificationCapabilities(specID, version, model string) Capabilities {
	return Capabilities{
		Protocol:         "v2",
		SpecIDs:          []string{specID},
		TaxonomyVersions: []string{version},
		PolicyVersions:   []string{classify.PolicyVersion},
		Models:           []string{model},
	}
}

// CompleteClassification commits suggestions only for the current input revision.
// The operation key is derived from the source revision so a retried commit after
// a lost response is idempotent at the Worker: it replays the stored result
// instead of paying for a second classification.
//
// A transient failure is genuinely uncertain: the commit may have succeeded and
// only the response was lost. The client therefore retries
// the *same* operation key with the *same* result, bounded and with backoff. It
// never re-runs inference, and it never fabricates a success (F10).
func (c *Client) CompleteClassification(ctx context.Context, job *ClassificationJob, result classify.Result) error {
	body := map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "input_revision": job.InputRevision,
		"target_generation": job.TargetGeneration, "spec_id": job.SpecID,
		"content_revision": job.ContentRevision, "evidence_hash": job.EvidenceHash,
		"operation_key": ClassificationOperationKey(job),
		"result":        result,
	}
	err := c.commitClassification(ctx, job.ID, body)
	if err == nil {
		return err
	}
	if enrich.ClassOf(err) != enrich.ErrorClassTransient {
		return err
	}
	for _, delay := range submitRecoveryBackoff {
		if ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		retryErr := c.commitClassification(ctx, job.ID, body)
		if retryErr == nil {
			return retryErr
		}
		if enrich.ClassOf(retryErr) != enrich.ErrorClassTransient {
			return retryErr
		}
		err = retryErr
	}
	// Still uncertain after the bounded recovery. The lease is left to expire
	// rather than re-running inference, and the caller records the uncertainty.
	return err
}

// submitRecoveryBackoff bounds how long an uncertain completion is retried.
var submitRecoveryBackoff = []time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second}

// Only a successful response to this exact operation/payload confirms it. A
// bookmark-level completed status or generic already_completed conflict could
// belong to a different operation and must never trigger successful follow-up.
func (c *Client) commitClassification(ctx context.Context, id int64, body any) error {
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/enrichment/classifications/%d/complete", id), body)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	var acknowledgment struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := decodeJSON(response.Body, &acknowledgment); err != nil {
		class := enrich.ErrorClassContract
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			class = enrich.ErrorClassTransient
		}
		return enrich.Classified(fmt.Errorf("decode classification acknowledgment: %w", err), class)
	}
	if acknowledgment.ID != id || acknowledgment.Status != "completed" {
		return enrich.Classified(errors.New("classification acknowledgment does not match the submitted operation"), enrich.ErrorClassContract)
	}
	return nil
}

// ClassificationOperationKey is deterministic for one input revision and target
// generation, which is exactly the scope of one classification result.
func ClassificationOperationKey(job *ClassificationJob) string {
	return fmt.Sprintf("classify-%d-rev-%d-input-%d-gen-%d", job.ID, job.Revision, job.InputRevision, job.TargetGeneration)
}

// FailClassification schedules durable backoff without changing saved content.
// A stale failure (superseded input or target) is reported with its revision so
// the Worker can avoid spending the new target's attempt budget.
func (c *Client) FailClassification(ctx context.Context, job *ClassificationJob, message string) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/fail", job.ID), map[string]any{
		"lease_token": job.LeaseToken, "revision": job.Revision, "input_revision": job.InputRevision,
		"target_generation": job.TargetGeneration, "error": message})
}

// RetryClassification enrolls a historical source or retries an inactive job.
// It only re-arms the classification queue: it never re-fetches the source and
// never calls a model.
func (c *Client) RetryClassification(ctx context.Context, id int64) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/classifications/%d/retry", id), map[string]any{})
}

// GetEvidenceAt loads one evidence snapshot by id so the consumer can evaluate
// exactly the material its lease was bound to (R2-07).
func (c *Client) GetEvidenceAt(ctx context.Context, id, snapshotID int64) (json.RawMessage, error) {
	if id < 1 || snapshotID < 1 {
		return nil, errors.New("bookmark and snapshot IDs must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/evidence?snapshot_id=%d", id, snapshotID), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode evidence snapshot: %w", err)
	}
	return payload, nil
}

// AckSourceRefresh consumes the one-shot refresh intent after the fetch attempt.
func (c *Client) AckSourceRefresh(ctx context.Context, id, epoch int64, status, reason string) error {
	body := map[string]any{"epoch": epoch, "status": status}
	if reason != "" {
		body["reason"] = reason
	}
	return c.stageWrite(ctx, fmt.Sprintf("/api/enrichment/jobs/%d/refresh-source/ack", id), body)
}

// RefreshSource schedules a bounded retrieval of the link's source. It is a
// distinct action from a classification retry (which never fetches) and from a
// policy replay (which never touches the network beyond the stored run): the
// Worker re-arms the retrieval queue while keeping the old readable content and
// human curation until new source bytes actually arrive (F13).
func (c *Client) RefreshSource(ctx context.Context, id int64) (json.RawMessage, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/enrichment/jobs/%d/refresh-source", id), map[string]any{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload json.RawMessage
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	return payload, nil
}

// StoredRun is one append-only classification run returned by the v2 API. It
// carries the raw judgments needed to re-decide without another model call.
// The answers field is decoded lazily by the caller so this package does not
// depend on the classify package's internal shapes.
type StoredRun struct {
	RawJudgments       json.RawMessage `json:"raw_judgments"`
	EvidenceSnapshotID int64           `json:"evidence_snapshot_id"`
	SourceHash         string          `json:"source_hash"`
	ID                 int64           `json:"id"`
	ContentRevision    int64           `json:"content_revision"`
	SpecID             string          `json:"spec_id"`
	SpecHash           string          `json:"spec_hash"`
	TargetGeneration   int64           `json:"target_generation"`
	RequestedModel     string          `json:"requested_model"`
	ResolvedModel      string          `json:"resolved_model"`
	PolicyVersion      string          `json:"policy_version"`
	// Policy is the historical policy payload stored with the run. A replay must
	// use it rather than substituting a default with the same version name.
	Policy           json.RawMessage `json:"policy"`
	Answers          json.RawMessage `json:"answers"`
	Usage            json.RawMessage `json:"usage"`
	Attempt          int             `json:"attempt"`
	OperationKey     string          `json:"operation_key"`
	Coverage         string          `json:"coverage"`
	EvidenceCoverage string          `json:"evidence_coverage"`
	AliasDrift       bool            `json:"alias_drift"`
	Status           string          `json:"status"`
	CreatedAt        string          `json:"created_at"`
}

// DecodeJudgments restores only recorded evaluation identities. The run row is
// authoritative for its database id; serialized metadata cannot choose it.
func (run StoredRun) DecodeJudgments(spec classify.QuestionSpec) (classify.RawJudgments, error) {
	if spec.SpecID != run.SpecID || spec.SemanticHash != run.SpecHash {
		return classify.RawJudgments{}, errors.New("stored run/spec identity mismatch")
	}
	raw, err := classify.RestoreStoredJudgments(spec, run.RequestedModel, run.ResolvedModel, run.Answers, run.Coverage, run.RawJudgments)
	if err != nil {
		return classify.RawJudgments{}, err
	}
	raw.SourceRunID = run.ID
	return raw, nil
}

// StoredQuestionSpec is the immutable question definition recovered for a
// replay. The payload is what the consumer compiled and registered, so the
// replay can rebuild the exact typed judgments of the run (F06).
type StoredQuestionSpec struct {
	SpecID         string          `json:"spec_id"`
	SpecHash       string          `json:"spec_hash"`
	SpecVersion    int             `json:"spec_version"`
	Payload        json.RawMessage `json:"payload"`
	RequestedModel string          `json:"requested_model,omitempty"`
	DisplayOnly    int             `json:"display_only,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	// Spec is the parsed form the Worker returns alongside the payload.
	Spec json.RawMessage `json:"spec,omitempty"`
}

// GetQuestionSpec loads the immutable question spec by id.
func (c *Client) GetQuestionSpec(ctx context.Context, specID string) (StoredQuestionSpec, error) {
	if strings.TrimSpace(specID) == "" {
		return StoredQuestionSpec{}, errors.New("spec id is required")
	}
	response, err := c.do(ctx, http.MethodGet, "/api/v2/question-specs/"+url.PathEscape(specID), nil)
	if err != nil {
		return StoredQuestionSpec{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return StoredQuestionSpec{}, apiError(response)
	}
	var stored StoredQuestionSpec
	if err := decodeJSON(response.Body, &stored); err != nil {
		return StoredQuestionSpec{}, fmt.Errorf("decode question spec: %w", err)
	}
	return stored, nil
}

// SubmitEvidence registers an immutable evidence snapshot before a run
// references it. It is idempotent for identical bytes.
func (c *Client) SubmitEvidence(ctx context.Context, id int64, snapshot any) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/v2/links/%d/evidence", id), map[string]any{"snapshot": snapshot})
}

// PutQuestionSpec registers the immutable compiled question set. The Worker
// rejects a different definition under the same id, so the semantic hash and
// the payload must be produced from the same spec.
func (c *Client) PutQuestionSpec(ctx context.Context, spec classify.QuestionSpec) error {
	payload, err := classify.MarshalSpec(spec)
	if err != nil {
		return fmt.Errorf("marshal question spec: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return fmt.Errorf("encode question spec: %w", err)
	}
	body["spec_hash"] = spec.SemanticHash
	return c.stageWrite(ctx, "/api/v2/question-specs", body)
}

// StoredDecision retains every input run of a policy replay. Older Workers only
// supplied RunID; that known reference remains readable but is not certified as
// the complete historical set. Personal revision can likewise be unknown.
type StoredDecision struct {
	ID                       int64           `json:"id"`
	RunID                    int64           `json:"run_id"`
	RunIDs                   []int64         `json:"run_ids"`
	RunReferencesComplete    bool            `json:"run_references_complete"`
	ExpectedPersonalRevision *int64          `json:"expected_personal_revision"`
	ContentRevision          int64           `json:"content_revision"`
	PolicyVersion            string          `json:"policy_version"`
	Policy                   json.RawMessage `json:"policy"`
	Automatic                json.RawMessage `json:"automatic"`
	CreatedAt                string          `json:"created_at"`
	SpecID                   string          `json:"spec_id"`
	SpecHash                 string          `json:"spec_hash"`
	RequestedModel           string          `json:"requested_model"`
	ResolvedModel            string          `json:"resolved_model"`
	Coverage                 string          `json:"coverage"`
}

// GetLatestDecision reads the durable policy result without model inference.
func (c *Client) GetLatestDecision(ctx context.Context, id int64) (*StoredDecision, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/decisions", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var decision StoredDecision
	if err := decodeJSON(response.Body, &decision); err != nil {
		return nil, fmt.Errorf("decode decision: %w", err)
	}
	if len(decision.RunIDs) == 0 && !decision.RunReferencesComplete {
		decision.RunIDs = []int64{decision.RunID}
	}
	if decision.ID < 1 || len(decision.RunIDs) == 0 || len(decision.RunIDs) > 64 {
		return nil, errors.New("invalid stored decision references")
	}
	seen := map[int64]bool{}
	for _, runID := range decision.RunIDs {
		if runID < 1 || seen[runID] {
			return nil, errors.New("invalid stored decision references")
		}
		seen[runID] = true
	}
	if !seen[decision.RunID] {
		return nil, errors.New("stored decision primary run is not referenced")
	}
	return &decision, nil
}

// SubmitDecision records a pure decision over stored runs. The Worker validates
// the run references and derives the effective view itself, so this call can
// never overwrite human curation with a stale or fabricated view.
func (c *Client) SubmitDecision(ctx context.Context, id int64, body map[string]any) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/v2/links/%d/decisions", id), body)
}

// GetLatestRun returns the newest recorded run, including a partial/failed one.
// The reuse caller checks eligibility; it must not silently skip a newer failure.
func (c *Client) GetLatestRun(ctx context.Context, id int64) (*StoredRun, error) {
	runs, err := c.GetRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[len(runs)-1], nil
}

// GetRuns returns the stored runs for a link, oldest first.
func (c *Client) GetRuns(ctx context.Context, id int64) ([]StoredRun, error) {
	if id < 1 {
		return nil, errors.New("bookmark ID must be positive")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/runs", id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, apiError(response)
	}
	var payload struct {
		Runs []StoredRun `json:"runs"`
	}
	if err := decodeJSON(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode runs: %w", err)
	}
	return payload.Runs, nil
}

// SubmitRun appends a run through the v2 API. It is idempotent by operation key.
func (c *Client) SubmitRun(ctx context.Context, id int64, body map[string]any) error {
	return c.stageWrite(ctx, fmt.Sprintf("/api/v2/links/%d/runs", id), body)
}

func (c *Client) stageWrite(ctx context.Context, path string, body any) error {
	response, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return apiError(response)
	}
	return nil
}
