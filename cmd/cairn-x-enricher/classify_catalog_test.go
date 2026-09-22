package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestClassifyCommandUsesSameMultidimensionalSpecAsService(t *testing.T) {
	legacy := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	full := legacy
	full.ContentFunctions = []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}
	full.Carriers = []taxonomy.Term{{ID: "single", Label: "Single", Active: true}}
	full.Affordances = []taxonomy.Term{{ID: "practice", Label: "Practice", Active: true}}
	spec, err := classify.CompileSpec(full, false)
	if err != nil {
		t.Fatal(err)
	}
	var v2Reads, legacyReads, claims atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/enrichment/taxonomy":
			legacyReads.Add(1)
			_ = json.NewEncoder(w).Encode(legacy)
		case "/api/v2/taxonomy":
			v2Reads.Add(1)
			_ = json.NewEncoder(w).Encode(full)
		case "/api/v2/question-specs":
			_ = json.NewEncoder(w).Encode(map[string]any{"spec_id": spec.SpecID, "spec_hash": spec.SemanticHash})
		case "/api/enrichment/classifications/target":
			_ = json.NewEncoder(w).Encode(map[string]any{"target": map[string]any{"generation": 1, "protocol": "v2", "spec_id": spec.SpecID, "spec_hash": spec.SemanticHash, "taxonomy_version": full.Version, "policy_version": classify.PolicyVersion, "requested_model": "jev-latest"}, "supported": r.URL.Query().Get("spec_ids") == spec.SpecID})
		case "/api/enrichment/classifications/claim":
			claims.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected command request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer server.Close()
	t.Setenv("CAIRN_API_BASE_URL", server.URL)
	t.Setenv("CAIRN_ENRICHER_TOKEN", "fixture")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	t.Setenv("TYPESAFE_API_KEY", "fixture")
	t.Setenv("TYPESAFE_MODEL", "jev-latest")
	command := newClassifyCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--max-jobs", "1"})
	if err := command.Execute(); err != nil {
		t.Fatalf("command cannot serve the multidimensional target: %v; v2 reads=%d legacy reads=%d", err, v2Reads.Load(), legacyReads.Load())
	}
	if v2Reads.Load() != 1 || legacyReads.Load() != 0 || claims.Load() != 1 {
		t.Fatalf("wrong catalog/claim path: v2=%d legacy=%d claims=%d", v2Reads.Load(), legacyReads.Load(), claims.Load())
	}
}

func TestClassifyCommandCatalogFallbackBoundaries(t *testing.T) {
	legacy := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}}, Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	spec, err := classify.CompileSpec(legacy, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		legacyStatus int
		fallback     bool
		wantErr      bool
	}{
		{"404 legacy", 404, `{"error":"not_found"}`, 200, true, false},
		{"405 legacy", 405, `{"error":"method_not_allowed"}`, 200, true, false},
		{"unauthorized", 401, `{"error":"unauthorized"}`, 200, false, true},
		{"forbidden", 403, `{"error":"forbidden"}`, 200, false, true},
		{"rate limited", 429, `{"error":"rate_limit"}`, 200, false, true},
		{"server failure", 500, `{"error":"backend_error"}`, 200, false, true},
		{"redirect", 302, `{"error":"redirect"}`, 200, false, true},
		{"bad json", 200, `{`, 200, false, true},
		{"bad vocabulary", 200, `{"version":"fixture","topics":[]}`, 200, false, true},
		{"unknown schema field", 200, `{"version":"fixture","unexpected_contract":true}`, 200, false, true},
		{"legacy load fails", 404, `{"error":"not_found"}`, 500, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var legacyReads, claims, writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v2/taxonomy":
					w.Header().Set("Location", "/api/enrichment/taxonomy")
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				case "/api/enrichment/taxonomy":
					legacyReads.Add(1)
					w.WriteHeader(tc.legacyStatus)
					_ = json.NewEncoder(w).Encode(legacy)
				case "/api/v2/question-specs":
					writes.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]string{"spec_id": spec.SpecID, "spec_hash": spec.SemanticHash})
				case "/api/enrichment/classifications/target":
					_ = json.NewEncoder(w).Encode(map[string]any{"target": map[string]any{"generation": 0, "protocol": "v2", "spec_id": spec.SpecID, "spec_hash": spec.SemanticHash, "taxonomy_version": legacy.Version, "policy_version": classify.PolicyVersion, "requested_model": "jev-latest"}, "supported": r.URL.Query().Get("spec_ids") == spec.SpecID})
				case "/api/enrichment/classifications/claim":
					claims.Add(1)
					w.WriteHeader(204)
				default:
					writes.Add(1)
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 404)
				}
			}))
			defer server.Close()
			t.Setenv("CAIRN_API_BASE_URL", server.URL)
			t.Setenv("CAIRN_ENRICHER_TOKEN", "fixture")
			t.Setenv("TYPESAFE_BASE_URL", server.URL)
			t.Setenv("TYPESAFE_API_KEY", "fixture")
			t.Setenv("TYPESAFE_MODEL", "jev-latest")
			cmd := newClassifyCommand()
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			args := []string{"--max-jobs", "1"}
			if tc.wantErr {
				args = append(args, "--id", "7")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
			wantLegacy := int64(0)
			if tc.fallback {
				wantLegacy = 1
			}
			if legacyReads.Load() != wantLegacy {
				t.Fatalf("legacy reads=%d want %d", legacyReads.Load(), wantLegacy)
			}
			if tc.wantErr {
				if claims.Load() != 0 || writes.Load() != 0 {
					t.Fatal("catalog failure changed queue or stored spec")
				}
				return
			}
			if claims.Load() != 1 {
				t.Fatal("legacy fallback did not drain the compatible queue")
			}
			if !strings.Contains(stderr.String(), "legacy classification vocabulary") {
				t.Fatal("fallback was not visible on stderr")
			}
			var result map[string]int64
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("stdout is not clean JSON: %v", err)
			}
		})
	}
}
