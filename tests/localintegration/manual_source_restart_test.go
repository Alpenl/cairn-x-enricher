package localintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/dashboard"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

const restartManualText = "A complete manually pasted source survives an abrupt process exit."

type neverRunManualSource struct{}

func (neverRunManualSource) Process(context.Context, *cairn.Job) error {
	return errors.New("the submission process must not execute a source job")
}
func (neverRunManualSource) ProcessWithSource(context.Context, *cairn.Job, string) error {
	return errors.New("the submission process must not execute a pasted source")
}

func TestLocalWorkerManualSourceSurvivesProcessExit(t *testing.T) {
	if os.Getenv("CAIRN_MANUAL_CHILD") == "1" {
		submitManualSourceThenExit(t)
		return
	}
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	before, err := queue.GetBookmark(ctx, id)
	if err != nil || before.CacheIdentity == nil {
		t.Fatalf("read initial content revision: %+v %v", before.CacheIdentity, err)
	}
	operation := fmt.Sprintf("restart-manual-%d", id)
	//nolint:gosec // Re-executes this test binary with a fixed test name and synthetic fixture values.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalWorkerManualSourceSurvivesProcessExit$")
	child.Env = append(os.Environ(), "CAIRN_MANUAL_CHILD=1",
		fmt.Sprintf("CAIRN_MANUAL_ID=%d", id),
		fmt.Sprintf("CAIRN_MANUAL_REVISION=%d", before.CacheIdentity.ContentRevision),
		"CAIRN_MANUAL_OPERATION="+operation)
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "accepted before exit") {
		t.Fatalf("submission process failed or did not accept: %v %s", err, output)
	}
	// A fresh client has no in-memory request or queue state from the child.
	restarted := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	detail, err := restarted.GetBookmark(ctx, id)
	if err != nil || detail.CacheIdentity == nil || detail.OriginalText != restartManualText ||
		detail.CacheIdentity.ContentRevision <= before.CacheIdentity.ContentRevision {
		t.Fatalf("pasted source after process exit: detail=%+v error=%v", detail, err)
	}
	identity, err := restarted.GetBookmarkIdentity(ctx, id)
	if err != nil || identity.CacheIdentity != *detail.CacheIdentity || identity.ID != id {
		t.Fatalf("small identity differs from detail after restart: %+v %v", identity, err)
	}
	reading, err := restarted.GetReading(ctx, id, nil)
	if err != nil || reading.Detail.OriginalText != restartManualText ||
		reading.Detail.CacheIdentity == nil || *reading.Detail.CacheIdentity != *detail.CacheIdentity ||
		reading.Selection.Revision != detail.CacheIdentity.PersonalRevision || !reading.Selection.Available {
		t.Fatalf("combined reading after restart: %+v %v", reading, err)
	}
	knownBodyRevision := detail.CacheIdentity.BodyRevision
	withoutBody, err := restarted.GetReading(ctx, id, &knownBodyRevision)
	if err != nil || !withoutBody.BodyUnchanged || withoutBody.Detail.OriginalText != "" ||
		withoutBody.Detail.CacheIdentity == nil || *withoutBody.Detail.CacheIdentity != *detail.CacheIdentity {
		t.Fatalf("matching body revision should omit the article: %+v %v", withoutBody, err)
	}
	server := dashboard.New(ctx, health.NewTracker(), restarted, neverRunManualSource{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	identityRequest := httptest.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("/api/bookmarks/%d/identity", id), nil)
	identityResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(identityResponse, identityRequest)
	if identityResponse.Code != http.StatusOK ||
		strings.Contains(identityResponse.Body.String(), restartManualText) {
		t.Fatalf("dashboard identity transferred body or failed: %d %s",
			identityResponse.Code, identityResponse.Body.String())
	}
	readingRequest := httptest.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("/api/bookmarks/%d/reading", id), nil)
	readingResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(readingResponse, readingRequest)
	if readingResponse.Code != http.StatusOK ||
		!strings.Contains(readingResponse.Body.String(), restartManualText) ||
		!strings.Contains(readingResponse.Body.String(), `"available":true`) {
		t.Fatalf("dashboard combined reading failed: %d %s",
			readingResponse.Code, readingResponse.Body.String())
	}
	source, err := restarted.GetSource(ctx, id)
	if err != nil || source == nil || source.OriginalText != restartManualText || source.Model != "manual" {
		t.Fatalf("archived source after process exit: %+v %v", source, err)
	}
	replayed, err := restarted.SaveManualSource(ctx, id, operation,
		before.CacheIdentity.ContentRevision, restartManualText)
	if err != nil || replayed.ContentRevision != detail.CacheIdentity.ContentRevision {
		t.Fatalf("lost-ack replay changed the source: %+v %v", replayed, err)
	}
	job, err := restarted.Claim(ctx)
	if err != nil || job == nil || job.ID != id || job.ContentRevision != detail.CacheIdentity.ContentRevision {
		t.Fatalf("restarted scheduler could not claim saved text: %+v %v", job, err)
	}
}

func submitManualSourceThenExit(t *testing.T) {
	id, idErr := strconv.ParseInt(os.Getenv("CAIRN_MANUAL_ID"), 10, 64)
	revision, revErr := strconv.ParseInt(os.Getenv("CAIRN_MANUAL_REVISION"), 10, 64)
	if idErr != nil || revErr != nil || id < 1 || revision < 1 {
		t.Fatal("invalid child fixture identity")
	}
	ctx := context.Background()
	queue := cairn.NewClient(workerURL(t), envOr("CAIRN_ENRICHER_TOKEN", "internal"),
		&http.Client{Timeout: 10 * time.Second})
	server := dashboard.New(ctx, health.NewTracker(), queue, neverRunManualSource{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)), 1)
	body, err := json.Marshal(map[string]any{
		"original_text": restartManualText, "operation_key": os.Getenv("CAIRN_MANUAL_OPERATION"),
		"expected_revision": revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("/api/bookmarks/%d/source", id), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("manual source HTTP %d: %s", response.Code, response.Body.String())
	}
	_, _ = os.Stdout.WriteString("accepted before exit\n")
	// Bypass Drain and all test defers: the process dies before the scheduler
	// starts reading work. Only the Worker's durable transaction can preserve it.
	os.Exit(0)
}
