package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// overviewTTL bounds how stale the navigation counts may be. The browser asks
// for them after every curation change and on a slow timer, while each refresh
// fans out to several upstream count queries.
const overviewTTL = 4 * time.Second

// overviewViews are the library views shown in the navigation. Each count is
// the Worker's own filtered total, so it follows exactly the same filter
// semantics as the list the view opens.
var overviewViews = []struct {
	name  string
	query cairn.BookmarkQuery
}{
	{"all", cairn.BookmarkQuery{}},
	{"inbox", cairn.BookmarkQuery{CurationStatus: "inbox"}},
	{"kept", cairn.BookmarkQuery{CurationStatus: "kept"}},
	{"compiled", cairn.BookmarkQuery{CurationStatus: "compiled"}},
	{"drop", cairn.BookmarkQuery{CurationStatus: "drop"}},
	{"uncertain", cairn.BookmarkQuery{Uncertain: true}},
}

type overviewSummary struct {
	Views map[string]int `json:"views"`
	Stale bool           `json:"stale,omitempty"`
	// Counts are the processing-state counts over the whole library.
	Counts cairn.BookmarkCounts `json:"counts"`
	// Attention is the number of bookmarks whose reading failed and that may
	// need a manual retry; Queued is what the processor has not finished yet.
	Attention int `json:"attention"`
	Queued    int `json:"queued"`
}

func (s *Server) getOverview(writer http.ResponseWriter, request *http.Request) {
	summary, stale, err := s.overview.read(request.Context(), s.requestCtx, overviewTTL, s.computeOverview)
	if err != nil {
		s.writeBackendError(writer, "build overview", 0, err)
		return
	}
	if stale {
		summary.Stale = true
		writer.Header().Set("Warning", `110 - "Response is stale"`)
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (s *Server) computeOverview(ctx context.Context) (overviewSummary, error) {
	type result struct {
		counts cairn.BookmarkCounts
		err    error
	}
	results := make([]result, len(overviewViews))
	var wait sync.WaitGroup
	for index, view := range overviewViews {
		wait.Add(1)
		go func() {
			defer wait.Done()
			query := view.query
			// Only the counts are used; one summary row keeps the page tiny.
			query.Limit = 1
			query.SummaryOnly = true
			page, err := s.backend.ListBookmarks(ctx, query)
			if err != nil {
				results[index] = result{err: fmt.Errorf("count %s bookmarks: %w", view.name, err)}
				return
			}
			results[index] = result{counts: page.Counts}
		}()
	}
	wait.Wait()

	summary := overviewSummary{Views: make(map[string]int, len(overviewViews))}
	for index, view := range overviewViews {
		if results[index].err != nil {
			return overviewSummary{}, results[index].err
		}
		summary.Views[view.name] = results[index].counts.Total
		if view.name == "all" {
			summary.Counts = results[index].counts
		}
	}
	summary.Attention = summary.Counts.Failed + summary.Counts.Exhausted
	summary.Queued = summary.Counts.Pending + summary.Counts.Processing
	return summary, nil
}

// invalidateOverview drops the cached counts after a local mutation, so the
// navigation reflects a status change on the very next read.
func (s *Server) invalidateOverview() {
	s.overview.invalidate()
}
