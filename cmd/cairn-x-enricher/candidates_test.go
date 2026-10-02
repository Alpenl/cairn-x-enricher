package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestCandidateProductionWiringIsExplicitAndNegotiatedBeforeProviderCalls(t *testing.T) {
	catalog := taxonomy.Catalog{Version: "fixture", Topics: []taxonomy.Term{
		{ID: "image_creation", Label: "图像生成", Active: true},
		{ID: "portrait_photography", Label: "写真", Active: true, Granularity: "specific", Description: "人物写真制作", Includes: []string{"人像写真"}, Excludes: []string{"配图"}, RecallTerms: []string{"写真"}},
	}, Forms: []taxonomy.Term{{ID: "method", Label: "方法", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "待试", Active: true}}}
	for _, tc := range []struct {
		name       string
		limit      int
		capability bool
		calls      int64
		wantError  bool
	}{
		{"default full coverage", 0, false, 0, false},
		{"core cannot fit", 2, true, 0, true},
		{"unsupported Worker", 4, false, 1, true},
		{"explicit supported recall", 4, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/enrichment/classifications/target" || r.Header.Get("X-Cairn-Candidate-Manifest") != "2" {
					t.Error("candidate configuration made a non-probe request")
				}
				if tc.capability {
					w.Header().Set("X-Cairn-Candidate-Manifest", "2")
				}
				_, _ = fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			client, err := classify.NewClient(server.URL, "fixture", "jev-1.13.0", server.Client(), catalog)
			if err != nil {
				t.Fatal(err)
			}
			configured, err := configureClassificationCandidates(context.Background(), config.Config{ClassificationCandidateMaxQuestions: tc.limit},
				cairn.NewClient(server.URL, "fixture", server.Client()), client)
			if (err != nil) != tc.wantError || requests.Load() != tc.calls {
				t.Fatalf("candidate preflight widened scope or paid: requests=%d err=%v", requests.Load(), err)
			}
			if !tc.wantError && configured.Spec().SemanticHash != client.Spec().SemanticHash {
				t.Fatal("candidate configuration changed the registered full spec")
			}
		})
	}
}

func TestCandidateCLIInvalidLimitFailsBeforeCredentialOrNetworkChecks(t *testing.T) {
	for _, value := range []string{"-1", "129"} {
		command := newClassifyCommand()
		command.SetArgs([]string{"--candidate-max-questions", value})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "candidate-max-questions") {
			t.Fatalf("invalid recall limit did not fail before configuration: %v", err)
		}
	}
}
