package localintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func policyFixtureRow(ctx context.Context, t *testing.T, sql string) map[string]any {
	t.Helper()
	root, config := os.Getenv("CAIRN_SHARE_ROOT"), os.Getenv("CAIRN_WRANGLER_CONFIG")
	if root == "" || config == "" {
		t.Fatal("isolated local D1 configuration required")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(root, "worker", "node_modules", ".bin", "wrangler"), "d1", "execute", "DB", "--local", "--config", config, "--command", sql, "--json") // #nosec G204 G702 -- only owned local integration configuration and synthetic SQL, never production.
	cmd.Dir = filepath.Join(root, "worker")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var envelope []struct {
		Results []map[string]any `json:"results"`
	}
	if json.Unmarshal(output, &envelope) != nil || len(envelope) != 1 || len(envelope[0].Results) != 1 {
		t.Fatalf("invalid local D1 audit: %s", output)
	}
	return envelope[0].Results[0]
}

func TestLocalWorkerAppPolicyPreviewAndLostReceipt(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "1")
	appToken, internal := envOr("CAIRN_APP_TOKEN", "app"), envOr("CAIRN_ENRICHER_TOKEN", "internal")
	queue := cairn.NewClient(base, internal, &http.Client{Timeout: 10 * time.Second})
	catalog := taxonomy.Catalog{Version: "2026-09-20.1", Topics: []taxonomy.Term{{ID: "llm", Label: "LLM", Active: true}},
		Forms: []taxonomy.Term{{ID: "method", Label: "Method", Active: true}}, Uses: []taxonomy.Term{{ID: "try", Label: "Try", Active: true}}}
	provider := providerContractServer(t, mustSpec(t, catalog))
	defer provider.Close()
	var modelCalls atomic.Int64
	providerHTTP := provider.Client()
	providerTransport := providerHTTP.Transport
	providerHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		modelCalls.Add(1)
		return providerTransport.RoundTrip(r)
	})
	classifier, err := classify.NewClient(provider.URL, "fixture", "jev-latest", providerHTTP, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.PutQuestionSpec(ctx, classifier.Spec()); err != nil {
		t.Fatal(err)
	}
	switchTarget(t, base, internal, classifier)
	id := createURLOnlyLink(t, base, appToken)
	image := fmt.Sprintf("enrichment/%d/%s.png", id, strings.Repeat("a", 64))
	localFixtureSQL(ctx, t, fmt.Sprintf(`UPDATE links SET original_text='Stored original source',enrichment_status='completed',
ai_title='Keep title',summary='Keep summary',translated_text='Keep translation',images='[{"key":"%s","content_type":"image/png"}]',
why='Private human reason',personal_revision=1 WHERE id=%d;
INSERT INTO curation_overrides(link_id,field,term,action,source,confirmed,revision,operation_key,created_at)
VALUES(%d,'use','contra','accept','human',1,1,'app-replay-human','2026-10-17');`, image, id, id))
	if err := queue.SubmitEvidence(ctx, id, map[string]any{"blocks": []map[string]any{{"id": "primary", "role": "primary", "text": "Stored original source", "acquired": "browser"}},
		"fetched_at": "2026-10-17", "retrieval": "browser", "truncation": map[string]any{"truncated": false}}); err != nil {
		t.Fatal(err)
	}
	reader := &localSourceReader{queue: queue}
	p := processor.NewStaged(queue, reader, classifier, catalog.Version, "jev-latest", slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if done, failed, err := p.RunClassifications(ctx, 1); err != nil || done != 1 || failed != 0 || modelCalls.Load() == 0 {
		t.Fatalf("failed initial contract classification: %d/%d %v", done, failed, err)
	}
	initialCalls := modelCalls.Load()
	var applyPosts, executorRequests atomic.Int64
	faultHTTP := &http.Client{Timeout: 10 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/app-policy-replays") && applyPosts.Add(1) == 1 {
			_ = response.Body.Close()
			return nil, errors.New("synthetic response loss after real policy decision commit")
		}
		return response, err
	})}
	executor := dashboard.New(ctx, nil, cairn.NewClient(base, internal, faultHTTP), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	executor.SetAppPolicyToken(internal)
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", os.Getenv("CAIRN_POLICY_EXECUTOR_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	executorHandler := executor.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executorRequests.Add(1)
		executorHandler.ServeHTTP(w, r)
	}))
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	send := func(origin, path, method, token string, body any) (int, []byte) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(cairn.PolicyPreviewCapability, "1")
		r.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
		if err != nil || len(data) > 256<<10 {
			t.Fatal("invalid bounded App response", err)
		}
		return response.StatusCode, data
	}
	path := fmt.Sprintf("/api/bookmarks/%d/policy-replay/", id)
	if status, _ := send(server.URL, fmt.Sprintf("/api/app-policy/bookmarks/%d/preview", id), "POST", appToken,
		map[string]any{"version": 1, "preview_key": "unauthorized"}); status != 401 {
		t.Fatal("App token reached private Go executor")
	}
	requestsBefore := executorRequests.Load()
	if status, _ := send(base, path+"capabilities", "GET", "wrong-app-token", nil); status != 401 || executorRequests.Load() != requestsBefore {
		t.Fatal("unauthorized App request reached executor")
	}
	if status, data := send(base, path+"capabilities", "GET", appToken, nil); status != 200 || !bytes.Contains(data, []byte(`"can_apply":true`)) {
		t.Fatalf("actual executor capability unavailable: %d %s", status, data)
	}
	statsSQL := fmt.Sprintf(`SELECT l.original_text,l.ai_title,l.summary,l.translated_text,l.images,l.why,l.note,l.classification,l.personal_revision,l.content_revision,
j.revision AS job_revision,j.input_revision,j.status AS job_status,
(SELECT COUNT(*) FROM classification_runs WHERE link_id=l.id) AS runs,
(SELECT COUNT(*) FROM classification_decisions WHERE link_id=l.id) AS decisions,
(SELECT COUNT(*) FROM classification_operations WHERE link_id=l.id) AS operations,
(SELECT COUNT(*) FROM classification_operations WHERE link_id=l.id AND status='app_policy_preview') AS previews,
(SELECT COUNT(*) FROM evidence_snapshots WHERE link_id=l.id) AS snapshots,
(SELECT COUNT(*) FROM curation_overrides WHERE link_id=l.id) AS overrides,
(SELECT COUNT(*) FROM curation_events WHERE link_id=l.id) AS events,
(SELECT COUNT(*) FROM classification_provider_attempts WHERE link_id=l.id) AS classification_calls,
(SELECT COUNT(*) FROM enrichment_provider_attempts WHERE link_id=l.id) AS reading_calls,
(SELECT COUNT(*) FROM budget_ledger) AS budget
FROM links l JOIN classification_jobs j ON j.link_id=l.id WHERE l.id=%d`, id)
	before := policyFixtureRow(ctx, t, statsSQL)
	mediaStatus, media := send(base, "/api/images/"+image, "GET", appToken, nil)
	if mediaStatus != 200 || len(media) == 0 {
		t.Fatal("actual R2 image missing")
	}
	mediaHash := sha256.Sum256(media)
	intent := map[string]any{"version": 1, "preview_key": "durable-policy-preview"}
	status, data := send(base, path+"preview", "POST", appToken, intent)
	var preview struct {
		Key   string         `json:"preview_key"`
		Hash  string         `json:"preview_hash"`
		After map[string]any `json:"after"`
	}
	if status != 200 || json.Unmarshal(data, &preview) != nil || preview.Hash == "" || preview.After["use"] != "contra" ||
		bytes.Contains(data, []byte(`"plan"`)) || bytes.Contains(data, []byte("Stored original source")) || bytes.Contains(data, []byte("Private human reason")) {
		t.Fatalf("actual public policy preview invalid or exposed internal material: %d %s", status, data)
	}
	afterPreview := policyFixtureRow(ctx, t, statsSQL)
	for key, value := range before {
		want := value
		if key == "previews" || key == "operations" {
			want = value.(float64) + 1
		}
		if !reflect.DeepEqual(afterPreview[key], want) {
			t.Fatalf("preview changed %s: %v != %v", key, afterPreview[key], want)
		}
	}
	if status, recovered := send(base, path+"preview", "POST", appToken, intent); status != 200 || !bytes.Equal(data, recovered) {
		t.Fatal("preparation recovery did not return the immutable original preview")
	}
	apply := map[string]any{"version": 1, "preview_key": preview.Key, "preview_hash": preview.Hash}
	if status, _ := send(base, path+"apply", "POST", appToken, apply); status != 503 || applyPosts.Load() != 1 {
		t.Fatal("lost actual commit response was retried or falsely confirmed")
	}
	afterApply := policyFixtureRow(ctx, t, statsSQL)
	for key, value := range afterPreview {
		want := value
		if key == "decisions" || key == "job_revision" {
			want = value.(float64) + 1
		}
		if !reflect.DeepEqual(afterApply[key], want) {
			t.Fatalf("approved replay changed %s unexpectedly: %v != %v", key, afterApply[key], want)
		}
	}
	localFixtureSQL(ctx, t, fmt.Sprintf("UPDATE links SET original_text='Newer source after approved replay',note='New human edit',personal_revision=personal_revision+1 WHERE id=%d; DELETE FROM classification_operations WHERE link_id=%d AND status='app_policy_preview';", id, id))
	t.Setenv("CAIRN_ALLOW_DECISION_WRITE", "0")
	beforeRecovery := policyFixtureRow(ctx, t, statsSQL)
	if status, data = send(base, path+"apply", "POST", appToken, apply); status != 200 || !bytes.Contains(data, []byte(`"replayed":true`)) || applyPosts.Load() != 1 {
		t.Fatalf("receipt recovery failed after source/human/flag and preview retention changes: %d %s", status, data)
	}
	if after := policyFixtureRow(ctx, t, statsSQL); !reflect.DeepEqual(beforeRecovery, after) {
		t.Fatal("receipt read changed newer source/human facts or caused another decision")
	}
	if status, latest := send(base, "/api/images/"+image, "GET", appToken, nil); status != 200 || sha256.Sum256(latest) != mediaHash {
		t.Fatal("policy replay changed actual R2 media")
	}
	if modelCalls.Load() != initialCalls || reader.transforms != 0 || reader.fetches != 0 {
		t.Fatal("policy replay invoked a model or source/reading processor")
	}
	t.Logf("Actual authenticated App Worker -> private Go executor -> Worker/D1/R2: immutable preview; one committed decision with lost response; same receipt after newer source/human edits, disabled writes and preview retention; zero additional model calls (setup contract calls=%d), source/reading/media/human/provider ledgers retained", initialCalls)
}
