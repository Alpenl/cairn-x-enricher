package localintegration

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

func TestLocalWorkerRefreshCheckpointConsumesIntent(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	id := createURLOnlyLink(t, base, "app")
	for range 2 {
		_, err := queue.RefreshSourceWithOperation(ctx, id, "retired-refresh")
		var apiErr *cairn.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 410 || apiErr.Code != "capture_required" {
			t.Fatalf("refresh was not retired: %v", err)
		}
	}
	if job, err := queue.Claim(ctx); err != nil || job != nil {
		t.Fatalf("uncaptured URL claimed: %+v %v", job, err)
	}
	detail, err := queue.GetBookmark(ctx, id)
	if err != nil || detail.OriginalText != "" || detail.Attempts != 0 || detail.Error != "capture_required" {
		t.Fatalf("URL-only state changed: %+v %v", detail, err)
	}
}
func TestLocalWorkerRefreshCheckpointSurvivesProcessExit(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	id := createLink(t, base, "app")
	_, _ = queue.RefreshSourceWithOperation(ctx, id, "retired-refresh")
	restarted := cairn.NewClient(base, "internal", &http.Client{Timeout: 10 * time.Second})
	job, err := restarted.Claim(ctx)
	if err != nil || job == nil || job.ID != id || job.RefreshEpoch != 0 {
		t.Fatalf("reading claim after restart: %+v %v", job, err)
	}
	detail, err := restarted.GetBookmark(ctx, id)
	if err != nil || detail.OriginalText != "Fixture original text" {
		t.Fatalf("archived original changed: %+v %v", detail, err)
	}
	if attempts := readProviderAttempts(ctx, t, base, "internal"); len(attempts) != 0 {
		t.Fatalf("refresh spent model permits: %+v", attempts)
	}
}
