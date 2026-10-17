package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
)

func previewPlan(t *testing.T) PolicyReplayRequest {
	t.Helper()
	policy := classify.DefaultPolicy()
	hash, err := classify.PolicyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	return PolicyReplayRequest{RunIDs: []int64{9}, PolicyVersion: policy.Version, Policy: policy, PolicyHash: hash,
		ContentRevision: 3, ExpectedRevision: 0, ExpectedTargetGeneration: 0, SpecID: "preview-spec", SpecHash: strings.Repeat("a", 64),
		RequestedModel: "recorded-model", ResolvedModel: "recorded-model", Automatic: classify.AutomaticFromProposals(classify.Proposals{
			Topics: []string{"ai_coding"}, ResourceKinds: []string{}, ContentFunctions: []string{}, Carriers: []string{}, Affordances: []string{}, Entities: []string{},
		})}
}

func previewResponse(t *testing.T) map[string]any {
	t.Helper()
	plan := previewPlan(t)
	plan.OperationKey = "app-policy:4:approved-key:" + strings.Repeat("b", 64)
	view := map[string]any{"topics": []string{}, "resource_kinds": []string{}, "content_functions": []string{}, "carriers": []string{},
		"affordances": []string{}, "entities": []string{}, "form": "", "use": ""}
	value := map[string]any{"version": 1, "id": 4, "preview_key": "approved-key", "preview_hash": strings.Repeat("b", 64),
		"status": "prepared", "model_calls": 0, "content_revision": 3, "personal_revision": 0, "target_generation": 0,
		"policy_version": plan.PolicyVersion, "policy_hash": plan.PolicyHash, "review_hash": strings.Repeat("c", 64),
		"plan": plan, "before": view, "after": view}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPolicyPreviewHTTPUsesExactInternalContractAndZeroGeneration(t *testing.T) {
	plan, response := previewPlan(t), previewResponse(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer internal-fixture" || r.Header.Get(PolicyPreviewCapability) != "1" {
			t.Error("missing exact internal authorization/capability")
		}
		w.Header().Set(PolicyPreviewCapability, "1")
		if r.Method == http.MethodGet {
			if r.URL.Path != "/api/v2/links/4/app-policy-previews" || r.URL.Query().Get("preview_key") != "approved-key" {
				t.Error("wrong scoped preview read")
			}
		} else {
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid JSON")
				return
			}
			if r.URL.Path == "/api/v2/links/4/app-policy-previews" {
				var fields map[string]json.RawMessage
				if json.Unmarshal(body["plan"], &fields) != nil || len(fields) != 12 || fields["operation_key"] != nil ||
					string(fields["expected_target_generation"]) != "0" || string(fields["expected_revision"]) != "0" {
					t.Error("preparation must omit the commit key and retain explicit zero versions")
				}
			} else if r.URL.Path != "/api/v2/links/4/app-policy-replays" || len(body) != 3 || body["plan"] != nil {
				t.Error("apply must send only the approved identity")
			}
		}
		result := response
		if r.URL.Path == "/api/v2/links/4/app-policy-replays" {
			result = map[string]any{"version": 1, "id": 4, "preview_key": "approved-key", "preview_hash": strings.Repeat("b", 64),
				"status": "applied", "model_calls": 0, "decision_id": 7, "revision": 0, "replayed": false,
				"policy_version": plan.PolicyVersion, "policy_hash": plan.PolicyHash, "target_generation": 0}
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "internal-fixture", server.Client())
	for _, action := range []func() (json.RawMessage, error){
		func() (json.RawMessage, error) {
			return client.GetPolicyPreview(context.Background(), 4, "approved-key", "")
		},
		func() (json.RawMessage, error) {
			return client.PreparePolicyPreview(context.Background(), 4, "approved-key", plan)
		},
		func() (json.RawMessage, error) {
			return client.ApplyPolicyPreview(context.Background(), 4, "approved-key", strings.Repeat("b", 64))
		},
	} {
		if _, err := action(); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected request count %d", calls.Load())
	}
}

func TestPolicyPreviewRejectsIncompleteOrForeignSuccessRecords(t *testing.T) {
	changes := map[string]func(map[string]any){
		"foreign owner":            func(v map[string]any) { v["id"] = 5 },
		"foreign nonce":            func(v map[string]any) { v["preview_key"] = "another" },
		"missing zero target":      func(v map[string]any) { delete(v, "target_generation") },
		"null target":              func(v map[string]any) { v["target_generation"] = nil },
		"paid response":            func(v map[string]any) { v["model_calls"] = 1 },
		"missing call proof":       func(v map[string]any) { delete(v, "model_calls") },
		"wrong commit identity":    func(v map[string]any) { v["plan"].(map[string]any)["operation_key"] = "other" },
		"null personal version":    func(v map[string]any) { v["plan"].(map[string]any)["expected_revision"] = nil },
		"omitted personal version": func(v map[string]any) { delete(v["plan"].(map[string]any), "expected_revision") },
		"missing after":            func(v map[string]any) { delete(v, "after") },
		"unconfirmed receipt":      func(v map[string]any) { v["status"] = "applied"; v["decision_id"] = 7; v["revision"] = 0 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			value := previewResponse(t)
			change(value)
			encoded, _ := json.Marshal(value)
			if validPolicyPreview(encoded, 4, "approved-key", "", nil, false) {
				t.Fatal("incomplete response accepted")
			}
		})
	}
	encoded, _ := json.Marshal(previewResponse(t))
	if validPolicyPreview(encoded, 4, "approved-key", "", nil, true) {
		t.Fatal("a prepared preview cannot confirm an apply request")
	}
}

func TestPolicyPreviewNeverFollowsRedirectsOrRetriesCommittedPost(t *testing.T) {
	var reached, writes atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	client := NewClient(server.URL, "internal-fixture", server.Client())
	if _, err := client.ApplyPolicyPreview(context.Background(), 4, "approved-key", strings.Repeat("b", 64)); err == nil {
		t.Fatal("unmarked redirect accepted")
	}
	server.Close()
	if reached.Load() != 0 || writes.Load() != 1 {
		t.Fatal("redirect followed or POST repeated")
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	client = NewClient(server.URL, "internal-fixture", server.Client())
	if _, err := client.ApplyPolicyPreview(context.Background(), 4, "approved-key", strings.Repeat("b", 64)); err == nil {
		t.Fatal("lost response accepted")
	}
	if writes.Load() != 2 {
		t.Fatal("ambiguous POST automatically retried")
	}
}

func TestPolicyPreviewUnmarkedRefusalsRemainUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"preview_changed"}`)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "fixture", server.Client()).ApplyPolicyPreview(context.Background(), 4, "approved-key", strings.Repeat("b", 64))
	var api *APIError
	if err == nil || errors.As(err, &api) {
		t.Fatal("unmarked response became a known refusal")
	}
}
