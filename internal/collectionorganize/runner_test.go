package collectionorganize

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

type queueFake struct {
	job         Job
	complete    map[string]any
	grant       extension.Grant
	budgetErr   error
	reservation extension.Reservation
}

func (q *queueFake) Collections(_ context.Context, _ string, path string, body any) (json.RawMessage, error) {
	if strings.HasSuffix(path, "/claim") {
		b, _ := json.Marshal(map[string]any{"job": q.job})
		return b, nil
	}
	q.complete = body.(map[string]any)
	return json.RawMessage(`{}`), nil
}
func (q *queueFake) ReserveExtensionBudget(_ context.Context, r extension.Reservation) (extension.Grant, error) {
	q.reservation = r
	return q.grant, q.budgetErr
}
func jobFixture() Job {
	return Job{Batch: "b", Run: "r", Version: "collection-fit-v1", Definitions: []Definition{{ID: "design", Name: "网站设计", Description: "网站改版"}, {ID: "health", Name: "健康"}}, Items: []Item{{ID: 1, Text: "网站界面参考"}, {ID: 2, Text: "营养方法"}}}
}
func TestJudgeBatchUsesOneIndependentNoulPerBookmarkAndCollection(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			State     map[string]any                       `json:"state"`
			Questions map[string]classify.ProviderQuestion `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Questions) != 4 || len(request.State["bookmarks"].(map[string]any)) != 2 {
			t.Error("incomplete judgment coverage")
		}
		answers := map[string]any{}
		for id, q := range request.Questions {
			if q.Type != "noul" {
				t.Error("wrong primitive")
			}
			answers[id] = map[string]any{"type": "noul", "noul": 0.92}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}})
	}))
	defer server.Close()
	judge, err := classify.NewJudgeClient(server.URL, "fixture", "jev-1.13.0", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	queue := &queueFake{job: jobFixture(), grant: extension.Grant{Granted: true, Reason: "reserved"}}
	r := Runner{Queue: queue, Judge: judge}
	if err = r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || queue.reservation.Kind != "collection_organize" || queue.reservation.Tokens != 65536 || len(queue.complete["results"].([]map[string]any)) != 2 {
		t.Fatal("batch/receipt not preserved")
	}
	if len(queue.complete["request_hash"].(string)) != 64 {
		t.Fatal("missing provenance")
	}
}
func TestBudgetFailureNeverCallsTheModel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))
	defer server.Close()
	judge, _ := classify.NewJudgeClient(server.URL, "fixture", "jev-1.13.0", server.Client())
	for _, test := range []struct {
		grant  extension.Grant
		err    error
		reason string
	}{{extension.Grant{Reason: "budget_exhausted"}, nil, "budget_exhausted"}, {extension.Grant{}, errors.New("lost grant"), "budget_response_unknown"}, {extension.Grant{Reason: "already_reserved"}, nil, "already_reserved"}} {
		queue := &queueFake{job: jobFixture(), grant: test.grant, budgetErr: test.err}
		r := Runner{Queue: queue, Judge: judge}
		if err := r.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if queue.complete["error"] != test.reason {
			t.Fatalf("wrong failure: %v", queue.complete)
		}
	}
	if calls != 0 {
		t.Fatal("ungranted model call")
	}
}
