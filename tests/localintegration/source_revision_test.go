package localintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

// Exercise the source checkpoint and immutable evidence through the production
// Go HTTP client against a real Worker/D1. No model boundary is invoked.
func TestLocalWorkerSourceRevisionOnce(t *testing.T) {
	base := workerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	queue := cairn.NewClient(base, envOr("CAIRN_ENRICHER_TOKEN", "internal"), &http.Client{Timeout: 10 * time.Second})
	id := createLink(t, base, envOr("CAIRN_APP_TOKEN", "app"))
	job, err := queue.ClaimByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	source := enrich.Source{OriginalText: "Synthetic source", OriginalLanguage: "en", ContextText: "initial context", RelatedLinks: []string{"https://example.com/initial"}, ImageURLs: []string{}, Model: "fixture"}
	type identity struct {
		ID       int64  `json:"id"`
		Revision int64  `json:"content_revision"`
		Hash     string `json:"content_hash"`
	}
	save := func() identity {
		t.Helper()
		if err := queue.SaveSource(ctx, id, job.LeaseToken, source); err != nil {
			t.Fatal(err)
		}
		if err := queue.SubmitEvidence(ctx, id, processor.EvidenceSnapshot(source, time.Now())); err != nil {
			t.Fatal(err)
		}
		raw, err := queue.GetEvidence(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var result identity
		if err := json.Unmarshal(raw, &result); err != nil || result.ID == 0 || result.Hash == "" {
			t.Fatalf("identity: %s %v", raw, err)
		}
		stored, err := queue.GetSource(ctx, id)
		if err != nil || stored == nil || !reflect.DeepEqual(*stored, source) {
			t.Fatalf("source roundtrip mismatch: %v", err)
		}
		return result
	}
	previous := save()
	initial := previous.Revision
	for step := 1; step <= 3; step++ {
		source.ContextText = fmt.Sprintf("context %d", step)
		source.RelatedLinks = []string{fmt.Sprintf("https://example.com/%d", step)}
		next := save()
		if next.Revision != previous.Revision+1 || next.ID == previous.ID || next.Hash == previous.Hash {
			t.Fatalf("compound update %d: before=%+v after=%+v", step, previous, next)
		}
		if replay := save(); replay != next {
			t.Fatalf("replay changed identity: before=%+v after=%+v", next, replay)
		}
		previous = next
	}
	source.ContextText = "invalid lease must not persist"
	if err := queue.SaveSource(ctx, id, "expired-lease", source); err == nil {
		t.Fatal("invalid lease accepted")
	}
	raw, err := queue.GetEvidence(ctx, id)
	var after identity
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &after); err != nil || after != previous {
		t.Fatalf("invalid lease changed evidence: %s %v", raw, err)
	}
	t.Logf("3 compound source changes: revision %d -> %d; repeated saves keep snapshot identity; invalid lease rejected; zero model calls", initial, previous.Revision)
}
