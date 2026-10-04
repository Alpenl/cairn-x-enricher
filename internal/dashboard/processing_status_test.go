package dashboard

import (
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

func TestProcessingPauseKeepsReadingAvailable(t *testing.T) {
	status := health.Snapshot{Ready: false, DegradedComponents: map[string]string{"source": "paused", "reading": "paused"}}
	if got := backstageTitle(status, 1); got != "自动处理暂时暂停" {
		t.Fatal(got)
	}
	state := backstageState(status, cairn.BookmarkCounts{Pending: 2}, 1)
	if !strings.Contains(state, "已归档正文仍可查看") || strings.Contains(state, "几分钟") {
		t.Fatal(state)
	}
	status.DegradedComponents["classification"] = "failed"
	if got := backstageTitle(status, 1); got != "服务未就绪" {
		t.Fatal(got)
	}
	if processingPaused(health.Snapshot{}) || processingPaused(health.Snapshot{Ready: true}) {
		t.Fatal("unknown or ready health must not be labelled as a processing pause")
	}
}
