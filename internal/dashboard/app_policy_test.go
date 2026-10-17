package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

// Contract backend with explicit prepared/applied stages. Actual Worker/D1
// atomicity is verified separately; this is not a fake always-Applied service.
type appPolicyFixture struct {
	*replayContractBackend
	key                            string
	target                         cairn.ClassificationTarget
	prepared                       json.RawMessage
	plan                           cairn.PolicyReplayRequest
	reads, prepares, applies, runs int
	loseResponse, applied          bool
}

func newAppPolicyFixture(t *testing.T) *appPolicyFixture {
	t.Helper()
	b := replayContractFixture(t)
	b.Backend = &fakeBackend{}
	b.run.Policy, _ = json.Marshal(classify.DefaultPolicy())
	return &appPolicyFixture{replayContractBackend: b, target: cairn.ClassificationTarget{Protocol: "v2", Generation: 2,
		SpecID: b.run.SpecID, SpecHash: b.run.SpecHash, TaxonomyVersion: "fixture", PolicyVersion: classify.PolicyVersion, RequestedModel: b.run.RequestedModel}}
}
func (b *appPolicyFixture) Handshake(_ context.Context, caps cairn.Capabilities) (cairn.HandshakeResult, error) {
	supported := slices.Contains(caps.SpecIDs, b.target.SpecID) && slices.Contains(caps.TaxonomyVersions, b.target.TaxonomyVersion) &&
		slices.Contains(caps.PolicyVersions, b.target.PolicyVersion) && slices.Contains(caps.Models, b.target.RequestedModel)
	return cairn.HandshakeResult{Target: b.target, Supported: supported}, nil
}
func (b *appPolicyFixture) GetReplayableRun(context.Context, int64) (*cairn.StoredRun, error) {
	b.runs++
	return &b.run, nil
}
func (b *appPolicyFixture) GetPolicyPreview(_ context.Context, id int64, key, hash string) (json.RawMessage, error) {
	b.reads++
	if b.prepared == nil || key != b.key {
		return nil, &cairn.APIError{StatusCode: 404, Code: "preview_not_found"}
	}
	if hash != "" && hash != strings.Repeat("a", 64) {
		return nil, &cairn.APIError{StatusCode: 409, Code: "operation_conflict"}
	}
	if b.applied {
		return b.receipt(id, key), nil
	}
	return b.prepared, nil
}
func (b *appPolicyFixture) PreparePolicyPreview(_ context.Context, id int64, key string, plan cairn.PolicyReplayRequest) (json.RawMessage, error) {
	b.prepares++
	b.plan = plan
	b.key = key
	view := map[string]any{"topics": []string{}, "resource_kinds": []string{}, "content_functions": []string{},
		"carriers": []string{}, "affordances": []string{}, "entities": []string{}, "form": "", "use": ""}
	b.prepared, _ = json.Marshal(map[string]any{"version": 1, "id": id, "preview_key": key, "preview_hash": strings.Repeat("a", 64),
		"status": "prepared", "model_calls": 0, "content_revision": plan.ContentRevision, "personal_revision": plan.ExpectedRevision,
		"target_generation": plan.ExpectedTargetGeneration, "policy_version": plan.PolicyVersion, "policy_hash": plan.PolicyHash,
		"before": view, "after": view, "plan": plan, "review_hash": strings.Repeat("b", 64)})
	return b.prepared, nil
}
func (b *appPolicyFixture) receipt(id int64, key string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"version": 1, "id": id, "preview_key": key, "preview_hash": strings.Repeat("a", 64),
		"status": "applied", "decision_id": 9, "revision": b.plan.ExpectedRevision, "target_generation": b.plan.ExpectedTargetGeneration,
		"policy_version": b.plan.PolicyVersion, "policy_hash": b.plan.PolicyHash, "replayed": true, "model_calls": 0})
	return payload
}
func (b *appPolicyFixture) ApplyPolicyPreview(_ context.Context, id int64, key, hash string) (json.RawMessage, error) {
	if key != b.key || hash != strings.Repeat("a", 64) {
		return nil, &cairn.APIError{StatusCode: 409, Code: "operation_conflict"}
	}
	b.applies++
	b.applied = true
	if b.loseResponse {
		return nil, errors.New("response lost after commit")
	}
	return b.receipt(id, key), nil
}
func appPolicyServer(t *testing.T, backend *appPolicyFixture) (*Server, func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	s := New(t.Context(), nil, backend, nil, testLogger(), 1)
	s.SetAppPolicyToken("internal-executor-token")
	return s, func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(cairn.PolicyPreviewCapability, "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
}

func TestAppPolicyExecutorPreparesOnlyItsDeployedPolicyAndRecoversBeforeRecomputing(t *testing.T) {
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "1")
	b := newAppPolicyFixture(t)
	s, send := appPolicyServer(t, b)
	reading, classification := make(chan struct{}, 4), make(chan struct{}, 4)
	s.SetWakeup(reading)
	s.SetClassificationWakeup(classification)
	path := "/api/app-policy/bookmarks/4/preview"
	first := send("POST", path, "internal-executor-token", `{"version":1,"preview_key":"original-intent"}`)
	if first.Code != 200 || b.prepares != 1 || b.runs != 1 || b.applies != 0 {
		t.Fatalf("unexpected preparation %d %s", first.Code, first.Body.String())
	}
	if b.plan.PolicyVersion != classify.PolicyVersion || b.plan.Policy != classify.DefaultPolicy() || b.plan.OperationKey != "" ||
		b.plan.ExpectedRevision != 41 || b.plan.ContentRevision != b.run.ContentRevision || b.plan.ExpectedTargetGeneration != b.target.Generation {
		t.Fatal("caller policy or guessed source/revisions entered the prepared plan")
	}
	var public map[string]json.RawMessage
	if json.Unmarshal(first.Body.Bytes(), &public) != nil || public["plan"] != nil || public["review_hash"] != nil || string(public["model_calls"]) != "0" {
		t.Fatal("internal plan was exposed or zero-call result omitted")
	}
	// A newer source/unsupported target must not change the original preview.
	b.run.Policy = nil
	b.target.PolicyVersion = "not-local"
	recovered := send("POST", path, "internal-executor-token", `{"version":1,"preview_key":"original-intent"}`)
	if recovered.Code != 200 || recovered.Body.String() != first.Body.String() || b.prepares != 1 || b.runs != 1 {
		t.Fatal("preview recovery recomputed the original intent")
	}
	b.loseResponse = true
	apply := `{"version":1,"preview_key":"original-intent","preview_hash":"` + strings.Repeat("a", 64) + `"}`
	if lost := send("POST", "/api/app-policy/bookmarks/4/apply", "internal-executor-token", apply); lost.Code != 503 || b.applies != 1 {
		t.Fatal("lost commit response falsely confirmed")
	}
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "0")
	if receipt := send("POST", "/api/app-policy/bookmarks/4/apply", "internal-executor-token", apply); receipt.Code != 200 || b.applies != 1 || b.runs != 1 {
		t.Fatal("confirmed receipt was blocked by mutable flags or invoked a new replay")
	}
	if len(reading)+len(classification) != 0 {
		t.Fatal("zero-model replay woke a paid consumer")
	}
}

func TestAppPolicyExecutorRequiresPrivateAuthAndRejectsManagementArguments(t *testing.T) {
	b := newAppPolicyFixture(t)
	_, send := appPolicyServer(t, b)
	path := "/api/app-policy/bookmarks/4/preview"
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"POST", path, "app-token", `{"version":1,"preview_key":"a"}`, 401},
		{"POST", path, "", `{"version":1,"preview_key":"a"}`, 401},
		{"POST", path, "internal-executor-token", `{"version":1,"preview_key":"a","policy":{}}`, 400},
		{"POST", path, "internal-executor-token", `{"version":1,"preview_key":"a","spec_id":"other"}`, 400},
		{"POST", path, "internal-executor-token", `{"version":1,"preview_key":"a","requested_model":"other"}`, 400},
		{"POST", path, "internal-executor-token", `{"version":1,"preview_key":"a","preview_hash":null}`, 400},
		{"POST", path, "internal-executor-token", `{"version":null,"preview_key":"a"}`, 400},
		{"GET", path + "?preview_key=a&unknown=x", "internal-executor-token", "", 400},
		{"GET", path + "?preview_key=a&preview_key=b", "internal-executor-token", "", 400},
		{"GET", "/api/app-policy/bookmarks/04/preview?preview_key=a", "internal-executor-token", "", 400},
		{"POST", path, "internal-executor-token", strings.Repeat(" ", 4097), 400},
	} {
		t.Run(fmt.Sprintf("%s-%s-%d", tc.method, tc.path, tc.status), func(t *testing.T) {
			if r := send(tc.method, tc.path, tc.token, tc.body); r.Code != tc.status {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
		})
	}
	if b.reads+b.prepares+b.applies+b.runs != 0 {
		t.Fatal("invalid request reached the privileged backend")
	}
}

func TestAppPolicyExecutorWriteFlagDoesNotDisableReadOrMisreportUnknownAsRefused(t *testing.T) {
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "0")
	b := newAppPolicyFixture(t)
	_, send := appPolicyServer(t, b)
	caps := send("GET", "/api/app-policy/bookmarks/4/capabilities", "internal-executor-token", "")
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"can_apply":false`) || b.runs != 0 {
		t.Fatal("capability read started work or falsely advertised writes")
	}
	if r := send("POST", "/api/app-policy/bookmarks/4/preview", "internal-executor-token", `{"version":1,"preview_key":"intent"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	body := `{"version":1,"preview_key":"intent","preview_hash":"` + strings.Repeat("a", 64) + `"}`
	if r := send("POST", "/api/app-policy/bookmarks/4/apply", "internal-executor-token", body); r.Code != 503 || b.applies != 0 {
		t.Fatal("write flag bypassed or unconfirmed old action claimed never executed")
	}
}

func TestAppPolicyExecutorNeverInventsHistoricalPolicyOrCrossesActiveTargetIdentity(t *testing.T) {
	for _, change := range []string{"history", "policy", "spec", "model"} {
		t.Run(change, func(t *testing.T) {
			b := newAppPolicyFixture(t)
			switch change {
			case "history":
				b.run.Policy = nil
			case "policy":
				b.target.PolicyVersion = "not-deployed-here"
			case "spec":
				b.target.SpecHash = strings.Repeat("f", 64)
			case "model":
				b.target.RequestedModel = "other-model"
			}
			_, send := appPolicyServer(t, b)
			r := send("POST", "/api/app-policy/bookmarks/4/preview", "internal-executor-token", `{"version":1,"preview_key":"intent"}`)
			if r.Code != 409 || b.prepares != 0 || b.applies != 0 {
				t.Fatalf("invalid historical or target binding accepted: %d %s", r.Code, r.Body.String())
			}
		})
	}
}
