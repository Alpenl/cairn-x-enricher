package dashboard

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

const (
	defaultPageSize  = 20
	maxPageSize      = 60
	maxManualBatch   = 10
	manualQueueDepth = 100
	maxSearchLength  = 200
	maxActionBody    = 4 << 10
	maxSourceBody    = 128 << 10
	// maxSourceLength is intentionally measured in bytes, matching the enrich
	// package's maxOriginalTextLength, which is also a byte limit. Both bound
	// the stored text size, so they must use the same unit.
	maxSourceLength = 100_000
)

var backstageAttentionStatuses = []string{"failed", "exhausted"}

//go:embed index.html
var indexHTML []byte

//go:embed reader.html
var readerHTML []byte

//go:embed backstage.html
var backstageHTML []byte

//go:embed dashboard.css
var dashboardCSS []byte

//go:embed common.js
var commonJS []byte

//go:embed home.js
var homeJS []byte

//go:embed backstage.js
var backstageJS []byte

//go:embed reader.js
var readerJS []byte

//go:embed curation-v2.js
var curationV2JS []byte

//go:embed reader-v2-panels.js
var readerV2PanelsJS []byte

//go:embed download.svg
var downloadSVG []byte

// Backend provides the internal Cloudflare data plane used by the dashboard.
type Backend interface {
	ListBookmarks(context.Context, cairn.BookmarkQuery) (cairn.BookmarkPage, error)
	GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error)
	GetImage(context.Context, string) (*http.Response, error)
	ClaimByID(context.Context, int64) (*cairn.Job, error)
	GetTaxonomy(context.Context) (taxonomy.Catalog, error)
	UpdateCuration(context.Context, int64, cairn.CurationUpdate) (cairn.BookmarkDetail, error)
}

// V2Backend is the optional multidimensional API. A backend that does not
// implement it degrades to read-only v1 rather than showing empty data.
type V2Backend interface {
	GetV2Selection(context.Context, int64) (cairn.V2SelectionView, error)
	UpdateV2Selection(context.Context, int64, cairn.V2SelectionUpdate) (cairn.V2SelectionView, error)
	GetV2Taxonomy(context.Context) (cairn.V2Taxonomy, error)
	ApplyV2Override(context.Context, int64, cairn.V2Override) (json.RawMessage, error)
	GetV2Effective(context.Context, int64) (json.RawMessage, error)
	// B05/B06/B09 surfaces: stored evidence, queue status, entity lifecycle,
	// the replayable run history and the three explicit redo actions.
	GetEvidence(context.Context, int64) (json.RawMessage, error)
	GetClassificationStatus(context.Context, int64) (json.RawMessage, error)
	GetEntities(context.Context, int64) (json.RawMessage, error)
	CorrectEntity(context.Context, int64, map[string]any) error
	GetRuns(context.Context, int64) ([]cairn.StoredRun, error)
	GetQuestionSpec(context.Context, string) (cairn.StoredQuestionSpec, error)
	SubmitDecision(context.Context, int64, map[string]any) error
	RetryClassification(context.Context, int64) error
	RefreshSource(context.Context, int64) (json.RawMessage, error)
}

// JobProcessor handles a job after the Worker has granted its lease.
type JobProcessor interface {
	Process(context.Context, *cairn.Job) error
	ProcessWithSource(context.Context, *cairn.Job, string) error
}

type manualJob struct {
	job        *cairn.Job
	sourceText string
}

// Server owns the management HTTP surface and bounded manual work queue.
type Server struct {
	// requestCtx is cancelled as soon as shutdown starts. Handlers use it so
	// they stop doing upstream work promptly.
	requestCtx context.Context
	// workerCtx is cancelled only once Drain has finished waiting for the
	// workers. Sharing one context for both would make the workers exit the
	// instant SIGTERM arrived, leaving admitted jobs unconsumed while Drain
	// waited for a counter that could never reach zero.
	workerCtx   context.Context
	stopWorkers context.CancelFunc
	tracker     *health.Tracker
	backend     Backend
	processor   JobProcessor
	logger      *slog.Logger
	jobs        chan manualJob
	workers     sync.WaitGroup

	// enqueueMu serialises admission so capacity cannot be oversold.
	enqueueMu sync.Mutex
	// draining is set under enqueueMu once shutdown starts, so admission is
	// refused even if a request arrives during the drain.
	draining bool
	// queued counts jobs admitted but not yet finished. It is updated under
	// enqueueMu at admission and atomically by workers, because workers
	// receive from the channel without holding that lock.
	queued  atomic.Int64
	catalog *taxonomyCache

	// extensionFlags reports which bounded extensions are enabled. They are
	// independent of each other and default to off.
	extensionFlags extension.Flags
	// extensions is the same service the pipeline uses, so a rerank action
	// shares its flags and budget.
	extensions *extension.Service

	// summary caches the backstage aggregate, which costs several backend
	// list calls and is polled by an idle browser tab.
	summaryMu       sync.Mutex
	summaryCache    *backstageSummary
	summaryCachedAt time.Time
}

// backstageSummaryTTL bounds backstage aggregation freshness. The page polls
// every few seconds, but the underlying queue changes far more slowly than
// that, and each refresh costs multiple upstream list calls.
const backstageSummaryTTL = 5 * time.Second

type backstageSummary struct {
	Title          string               `json:"title"`
	State          string               `json:"state"`
	LastError      string               `json:"last_error,omitempty"`
	Attention      []cairn.Bookmark     `json:"attention"`
	AttentionTotal int                  `json:"attention_total"`
	Counts         cairn.BookmarkCounts `json:"counts"`
	Build          buildinfo.Info       `json:"build"`
}

// New creates a dashboard and starts bounded manual processing workers.
//
// ctx governs request handling and scheduler-style work; the workers get their
// own context so Drain can keep them running until the queue empties.
func New(
	ctx context.Context,
	tracker *health.Tracker,
	backend Backend,
	jobProcessor JobProcessor,
	logger *slog.Logger,
	workerCount int,
) *Server {
	if workerCount < 1 {
		workerCount = 1
	}
	// Detached from ctx on purpose: Drain cancels this once the queue is empty.
	// The signal context is already cancelled by the time shutdown starts.
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	server := &Server{
		requestCtx:  ctx,
		workerCtx:   workerCtx,
		stopWorkers: stopWorkers,
		tracker:     tracker,
		backend:     backend,
		processor:   jobProcessor,
		logger:      logger,
		jobs:        make(chan manualJob, manualQueueDepth),
		catalog:     newTaxonomyCache(backend),
	}
	for range workerCount {
		server.workers.Add(1)
		go server.runWorker()
	}
	return server
}

// Drain stops admitting new manual work and waits up to timeout for jobs that
// were already leased to finish. Without this, an in-flight manual job would
// be abandoned on shutdown and waste its lease and attempt budget.
//
// Workers keep running during the wait and are stopped afterwards, so an
// admitted job is always consumed rather than stranded in the channel.
//
// The wait is best-effort: a single model request is bounded by REQUEST_TIMEOUT
// and may exceed the remaining shutdown budget. That is safe rather than
// silent, because the lease is never acknowledged, so the Worker re-issues the
// job once the lease expires.
func (s *Server) Drain(timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	// Refuse new work first so the queue can only shrink from here.
	s.enqueueMu.Lock()
	s.draining = true
	s.enqueueMu.Unlock()

	if pending := s.queued.Load(); pending > 0 && timeout > 0 {
		s.logger.Info("draining manual jobs", "pending", pending, "timeout", timeout)
		s.waitForQueueToEmpty(deadline)
		if pending := s.queued.Load(); pending > 0 {
			s.logger.Warn("shutdown drain timed out; unfinished jobs keep their lease and will be retried",
				"pending", pending)
		}
	}

	// Stop the workers and wait for them to observe it, so no goroutine is
	// still touching the processor after Drain returns.
	//
	// Cancelling workerCtx also releases a worker blocked on an empty queue, so
	// this normally completes immediately. The wait is capped both by a fixed
	// floor and by whatever remains of the caller's budget, so a worker stuck
	// in a stage that ignores cancellation cannot hold the process open past
	// the shutdown budget.
	s.stopWorkers()
	workerWait := min(shutdownWaitForWorkers, max(time.Until(deadline), 0))
	if !waitForWorkers(&s.workers, workerWait) {
		s.logger.Warn("workers did not stop within the drain budget; " +
			"they will be terminated with the process")
	}
}

// shutdownWaitForWorkers bounds how long Drain waits for worker goroutines to
// observe cancellation after being asked to stop. They normally exit at once.
const shutdownWaitForWorkers = 2 * time.Second

// drainPollInterval is how often the drain rechecks the pending counter. It is
// a ticker rather than a bare sleep so the wait reacts promptly to the last job
// finishing without spinning.
const drainPollInterval = 20 * time.Millisecond

// waitForQueueToEmpty polls the pending counter until it reaches zero or the
// deadline passes. It is deliberately not cancellable: during shutdown there is
// nothing left to cancel it with, and the deadline is the bound.
func (s *Server) waitForQueueToEmpty(deadline time.Time) {
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for s.queued.Load() > 0 {
		if !time.Now().Before(deadline) {
			return
		}
		<-ticker.C
	}
}

// waitForWorkers reports whether all tracked goroutines finished in time.
func waitForWorkers(group *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		group.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Handler returns the complete health and management HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(writer http.ResponseWriter, _ *http.Request) {
		servePage(writer, indexHTML)
	})
	mux.HandleFunc("GET /bookmarks/{id}", serveReader)
	mux.HandleFunc("GET /backstage", func(writer http.ResponseWriter, _ *http.Request) {
		servePage(writer, backstageHTML)
	})
	mux.HandleFunc("GET /assets/dashboard.css", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/css; charset=utf-8", dashboardCSS)
	})
	mux.HandleFunc("GET /assets/common.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", commonJS)
	})
	mux.HandleFunc("GET /assets/home.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", homeJS)
	})
	mux.HandleFunc("GET /assets/backstage.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", backstageJS)
	})
	mux.HandleFunc("GET /assets/reader.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", readerJS)
	})
	mux.HandleFunc("GET /assets/curation-v2.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", curationV2JS)
	})
	mux.HandleFunc("GET /assets/reader-v2-panels.js", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "text/javascript; charset=utf-8", readerV2PanelsJS)
	})
	mux.HandleFunc("GET /assets/download.svg", func(writer http.ResponseWriter, _ *http.Request) {
		serveAsset(writer, "image/svg+xml", downloadSVG)
	})

	healthHandler := s.tracker.Handler()
	mux.Handle("GET /healthz", healthHandler)
	mux.Handle("GET /readyz", healthHandler)
	mux.Handle("GET /status", healthHandler)

	mux.HandleFunc("GET /api/bookmarks", s.listBookmarks)
	mux.HandleFunc("GET /api/taxonomy", s.getTaxonomy)
	mux.HandleFunc("PATCH /api/bookmarks/{id}/curation", s.updateCuration)
	mux.HandleFunc("GET /api/bookmarks/{id}/v2-selection", s.getV2Selection)
	mux.HandleFunc("PATCH /api/bookmarks/{id}/v2-selection", s.updateV2Selection)
	mux.HandleFunc("GET /api/v2-taxonomy", s.getV2Taxonomy)
	mux.HandleFunc("GET /api/extensions", s.getExtensions)
	mux.HandleFunc("POST /api/bookmarks/{id}/v2-override", s.applyV2Override)
	mux.HandleFunc("GET /api/bookmarks/{id}/v2-effective", s.getV2Effective)
	mux.HandleFunc("GET /api/bookmarks/{id}/evidence", s.getEvidence)
	mux.HandleFunc("GET /api/bookmarks/{id}/classification-status", s.getClassificationStatus)
	mux.HandleFunc("GET /api/bookmarks/{id}/entities", s.getEntities)
	mux.HandleFunc("POST /api/bookmarks/{id}/entities", s.correctEntity)
	mux.HandleFunc("POST /api/bookmarks/{id}/retry-classification", s.retryClassification)
	mux.HandleFunc("POST /api/bookmarks/{id}/refresh-source", s.refreshSource)
	mux.HandleFunc("POST /api/bookmarks/{id}/replay-policy", s.replayPolicy)
	mux.HandleFunc("GET /api/export", s.exportMarkdown)
	mux.HandleFunc("POST /api/rerank", s.rerank)
	mux.HandleFunc("GET /api/bookmarks/{id}", s.getBookmark)
	mux.HandleFunc("GET /api/images/{key...}", s.getImage)
	mux.HandleFunc("GET /api/backstage", s.getBackstage)
	mux.HandleFunc("POST /api/bookmarks/process", s.processBookmarks)
	mux.HandleFunc("POST /api/bookmarks/{id}/source", s.processBookmarkSource)
	return mux
}

func serveReader(writer http.ResponseWriter, request *http.Request) {
	if _, err := positiveID(request.PathValue("id")); err != nil {
		http.NotFound(writer, request)
		return
	}
	servePage(writer, readerHTML)
}

func servePage(writer http.ResponseWriter, content []byte) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func serveAsset(writer http.ResponseWriter, contentType string, content []byte) {
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (s *Server) listBookmarks(writer http.ResponseWriter, request *http.Request) {
	query, err := bookmarkQuery(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query")
		return
	}
	page, err := s.backend.ListBookmarks(request.Context(), query)
	if err != nil {
		s.writeBackendError(writer, "list bookmarks", 0, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) getBookmark(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	detail, err := s.backend.GetBookmark(request.Context(), id)
	if err != nil {
		s.writeBackendError(writer, "get bookmark", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (s *Server) getTaxonomy(writer http.ResponseWriter, request *http.Request) {
	catalog, err := s.catalog.Catalog(request.Context())
	if err != nil {
		s.writeBackendError(writer, "get taxonomy", 0, err)
		return
	}
	writeJSON(writer, http.StatusOK, catalog)
}

func (s *Server) updateCuration(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var update cairn.CurationUpdate
	if err := decoder.Decode(&update); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if (update.Why == nil && update.Status == nil && update.Classification == nil) ||
		(update.Why != nil && utf8.RuneCountInString(*update.Why) > 200) ||
		(update.Status != nil && !taxonomy.ValidCurationStatus(*update.Status)) {
		writeError(writer, http.StatusBadRequest, "invalid_curation")
		return
	}
	if update.Classification != nil && string(update.Classification) != "null" {
		var selection taxonomy.Selection
		selectionDecoder := json.NewDecoder(strings.NewReader(string(update.Classification)))
		selectionDecoder.DisallowUnknownFields()
		if err := selectionDecoder.Decode(&selection); err != nil || selection.Topics == nil {
			writeError(writer, http.StatusBadRequest, "invalid_curation")
			return
		}
		catalog, err := s.catalog.Catalog(request.Context())
		if err != nil {
			s.writeBackendError(writer, "get taxonomy for curation", id, err)
			return
		}
		if err := catalog.ValidateSelection(selection); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_curation")
			return
		}
	}
	detail, err := s.backend.UpdateCuration(request.Context(), id, update)
	if err != nil {
		s.writeBackendError(writer, "update curation", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

// v2Backend returns the optional multidimensional API, or writes a safe
// read-only signal when the configured backend does not implement it.
func (s *Server) v2Backend(writer http.ResponseWriter) (V2Backend, bool) {
	v2, ok := s.backend.(V2Backend)
	if !ok {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return nil, false
	}
	return v2, true
}

// SetExtensions attaches the bounded extension service.
func (s *Server) SetExtensions(service *extension.Service) {
	s.extensions = service
	if service != nil {
		s.extensionFlags = service.Flags
	}
}

// getExtensions reports which bounded semantic extensions are enabled. Every
// flag defaults to off, so a disabled extension is visible rather than a
// button that silently succeeds.
func (s *Server) getExtensions(writer http.ResponseWriter, _ *http.Request) {
	flags := s.extensionFlags
	writeJSON(writer, http.StatusOK, map[string]any{
		"entities": flags.Entities, "evidence": flags.Evidence,
		"rerank": flags.Rerank, "proposal": flags.Proposal,
		"quality_verified": false,
	})
}

func (s *Server) getV2Selection(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	view, err := v2.GetV2Selection(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get v2 selection", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"available": true, "selection": view.Selection, "automatic": view.Automatic, "v1_projection": view.V1Projection, "taxonomy_version": view.TaxonomyVersion, "v1_only": view.V1Only, "revision": view.Revision})
}

func (s *Server) updateV2Selection(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var selection cairn.V2SelectionUpdate
	if err := decoder.Decode(&selection); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	view, err := v2.UpdateV2Selection(request.Context(), id, selection)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusConflict, "v2_unsupported")
		return
	}
	if err != nil {
		s.writeBackendError(writer, "update v2 selection", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getV2Taxonomy(writer http.ResponseWriter, request *http.Request) {
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	vocabulary, err := v2.GetV2Taxonomy(request.Context())
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get v2 taxonomy", 0, err)
		return
	}
	writeJSON(writer, http.StatusOK, vocabulary)
}

func (s *Server) applyV2Override(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var override cairn.V2Override
	if err := decoder.Decode(&override); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	// A why/status edit must never produce an override event: only a real
	// field-level action is accepted here.
	if override.Field == "why" || override.Field == "status" || override.OperationKey == "" {
		writeError(writer, http.StatusBadRequest, "invalid_override")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	result, err := v2.ApplyV2Override(request.Context(), id, override)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeError(writer, http.StatusConflict, "v2_unsupported")
		return
	}
	if err != nil {
		s.writeBackendError(writer, "apply v2 override", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) getV2Effective(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	v2, ok := s.v2Backend(writer)
	if !ok {
		return
	}
	result, err := v2.GetV2Effective(request.Context(), id)
	if errors.Is(err, cairn.ErrV2Unsupported) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false, "reason": "v2_unsupported"})
		return
	}
	if err != nil {
		s.writeBackendError(writer, "get v2 effective", id, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) getImage(writer http.ResponseWriter, request *http.Request) {
	response, err := s.backend.GetImage(request.Context(), request.PathValue("key"))
	if err != nil {
		s.writeBackendError(writer, "get image", 0, err)
		return
	}
	defer func() { _ = response.Body.Close() }()

	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		s.logger.Error("get image returned unsafe content type", "content_type", contentType)
		writeError(writer, http.StatusBadGateway, "backend_error")
		return
	}
	for _, header := range []string{"Content-Type", "Content-Length", "ETag", "Cache-Control", "Last-Modified"} {
		if value := response.Header.Get(header); value != "" {
			writer.Header().Set(header, value)
		}
	}
	// Image keys are content-addressed (enrichment/<id>/<sha256>.<ext>), so a
	// given key can never change. Without a Cache-Control the browser falls back
	// to heuristic caching, which revalidates on every scroll. Only apply this
	// when the backend did not set its own directive.
	if writer.Header().Get("Cache-Control") == "" {
		writer.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	}
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	// When the backend advertises a length, copy exactly that many bytes so a
	// truncated upstream response surfaces as a logged error instead of a
	// silently corrupt image collected by the browser cache.
	if declared, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64); err == nil && declared > 0 {
		written, copyErr := io.CopyN(writer, response.Body, declared)
		if copyErr != nil || written != declared {
			s.logger.WarnContext(request.Context(), "truncated image response",
				"declared", declared, "written", written, "error", copyErr)
		}
		return
	}
	if _, err := io.Copy(writer, response.Body); err != nil {
		s.logger.WarnContext(request.Context(), "stream image response", "error", err)
	}
}

func (s *Server) getBackstage(writer http.ResponseWriter, request *http.Request) {
	summary, err := s.buildBackstageSummary(request.Context())
	if err != nil {
		s.writeBackendError(writer, "build backstage summary", 0, err)
		return
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (s *Server) buildBackstageSummary(ctx context.Context) (backstageSummary, error) {
	s.summaryMu.Lock()
	defer s.summaryMu.Unlock()
	if s.summaryCache != nil && time.Since(s.summaryCachedAt) < backstageSummaryTTL {
		return *s.summaryCache, nil
	}

	status := s.tracker.Snapshot()
	// A filtered list already carries queue-wide counts, so one request per
	// attention status replaces the previous extra unfiltered counts call.
	attention := []cairn.Bookmark{}
	var counts cairn.BookmarkCounts
	for index, name := range backstageAttentionStatuses {
		page, err := s.backend.ListBookmarks(ctx, cairn.BookmarkQuery{Limit: 20, Status: name})
		if err != nil {
			return backstageSummary{}, fmt.Errorf("list %s bookmarks: %w", name, err)
		}
		if index == 0 {
			counts = page.Counts
		} else {
			counts = mergeBookmarkCounts(counts, page.Counts)
		}
		attention = append(attention, page.Items...)
	}

	attentionTotal := counts.Failed + counts.Exhausted
	if attentionTotal == 0 {
		attentionTotal = len(attention)
	}
	summary := backstageSummary{
		Title:          backstageTitle(status, attentionTotal),
		State:          backstageState(status, counts, attentionTotal),
		LastError:      status.LastError,
		Attention:      attention,
		AttentionTotal: attentionTotal,
		Counts:         counts,
		Build:          status.Build,
	}
	s.summaryCache = &summary
	s.summaryCachedAt = time.Now()
	return summary, nil
}

func mergeBookmarkCounts(left, right cairn.BookmarkCounts) cairn.BookmarkCounts {
	return cairn.BookmarkCounts{
		Total:       max(left.Total, right.Total),
		Pending:     max(left.Pending, right.Pending),
		Processing:  max(left.Processing, right.Processing),
		Completed:   max(left.Completed, right.Completed),
		Failed:      max(left.Failed, right.Failed),
		Exhausted:   max(left.Exhausted, right.Exhausted),
		Unsupported: max(left.Unsupported, right.Unsupported),
	}
}

func backstageTitle(status health.Snapshot, attentionTotal int) string {
	if !status.Ready {
		return "服务未就绪"
	}
	if attentionTotal > 0 {
		return fmt.Sprintf("需要处理 %d 条", attentionTotal)
	}
	if status.LastError != "" {
		return "最近一批有错误"
	}
	return "一切正常"
}

func backstageState(status health.Snapshot, counts cairn.BookmarkCounts, attentionTotal int) string {
	parts := []string{}
	if status.LastWorkStats != nil {
		stats := status.LastWorkStats
		parts = append(parts, fmt.Sprintf("最近一次实际处理领取 %d 条，完成 %d 条，失败 %d 条。", stats.Claimed, stats.Completed, stats.Failed))
		if status.LastStats != nil && !status.LastStats.HasWork() {
			parts = append(parts, "最近一批没有领取到新任务。")
		}
	} else if status.LastStats != nil {
		if status.LastStats.HasWork() {
			stats := status.LastStats
			parts = append(parts, fmt.Sprintf("最近一批领取 %d 条，完成 %d 条，失败 %d 条。", stats.Claimed, stats.Completed, stats.Failed))
		} else {
			parts = append(parts, "最近一批没有领取到新任务。")
		}
	} else {
		parts = append(parts, "服务已启动，尚未记录处理批次。")
	}
	if queued := counts.Pending + counts.Processing; queued > 0 {
		parts = append(parts, fmt.Sprintf("队列里还有 %d 条在等待处理。", queued))
	}
	if attentionTotal > 0 {
		parts = append(parts, fmt.Sprintf("还有 %d 条需要人工处理。", attentionTotal))
	}
	parts = append(parts, "新收藏一般在几分钟内出现在列表里，平时不需要打开这一页。")
	return strings.Join(parts, "")
}

func (s *Server) processBookmarks(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxActionBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	ids, err := uniqueIDs(body.IDs)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_ids")
		return
	}

	accepted := make([]int64, 0, len(ids))
	rejected := make([]rejection, 0)

	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if s.draining {
		writeError(writer, http.StatusServiceUnavailable, "shutting_down")
		return
	}
	if s.queued.Load()+int64(len(ids)) > int64(cap(s.jobs)) {
		writeError(writer, http.StatusServiceUnavailable, "queue_full")
		return
	}
	for _, id := range ids {
		job, claimErr := s.backend.ClaimByID(request.Context(), id)
		if claimErr != nil {
			code := publicErrorCode(claimErr)
			rejected = append(rejected, rejection{ID: id, Error: code})
			s.logger.WarnContext(request.Context(), "manual claim rejected", "link_id", id, "error", claimErr)
			continue
		}
		if job == nil {
			rejected = append(rejected, rejection{ID: id, Error: "not_found"})
			continue
		}
		s.queued.Add(1)
		s.jobs <- manualJob{job: job}
		accepted = append(accepted, id)
	}

	status := http.StatusAccepted
	if len(accepted) == 0 {
		status = http.StatusConflict
	}
	writeJSON(writer, status, map[string]any{
		"accepted": accepted,
		"rejected": rejected,
	})
}

func (s *Server) processBookmarkSource(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_id")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "invalid_content_type")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxSourceBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		OriginalText string `json:"original_text"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json")
		return
	}
	sourceText := strings.TrimSpace(body.OriginalText)
	if sourceText == "" || len(sourceText) > maxSourceLength {
		writeProcessingResult(writer, http.StatusConflict, nil, []rejection{{ID: id, Error: "invalid_source"}})
		return
	}

	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if s.draining {
		writeError(writer, http.StatusServiceUnavailable, "shutting_down")
		return
	}
	if s.queued.Load()+1 > int64(cap(s.jobs)) {
		writeError(writer, http.StatusServiceUnavailable, "queue_full")
		return
	}
	job, claimErr := s.backend.ClaimByID(request.Context(), id)
	if claimErr != nil {
		code := publicErrorCode(claimErr)
		s.logger.WarnContext(request.Context(), "manual source claim rejected", "link_id", id, "error", claimErr)
		writeProcessingResult(writer, http.StatusConflict, nil, []rejection{{ID: id, Error: code}})
		return
	}
	if job == nil {
		writeProcessingResult(writer, http.StatusConflict, nil, []rejection{{ID: id, Error: "not_found"}})
		return
	}
	s.queued.Add(1)
	s.jobs <- manualJob{job: job, sourceText: sourceText}
	writeProcessingResult(writer, http.StatusAccepted, []int64{id}, nil)
}

// runJobSafely executes one manual job, converting a panic into an error so
// the caller's normal failure reporting still runs.
func (s *Server) runJobSafely(queued manualJob) (err error) {
	defer processor.RecoverJob(s.logger, "manual enrichment", queued.job.ID, &err)
	if queued.sourceText == "" {
		return s.processor.Process(s.workerCtx, queued.job)
	}
	return s.processor.ProcessWithSource(s.workerCtx, queued.job, queued.sourceText)
}

// runWorker is the per-worker loop.
//
// Workers exit only when the process context is cancelled. During shutdown
// Drain runs first, so queued jobs are completed rather than abandoned
// mid-lease.
func (s *Server) runWorker() {
	defer s.workers.Done()
	for {
		select {
		case <-s.workerCtx.Done():
			return
		case queued := <-s.jobs:
			s.runManualJob(queued)
		}
	}
}

func (s *Server) runManualJob(queued manualJob) {
	defer s.queued.Add(-1)
	job := queued.job
	started := time.Now().UTC()
	stats := processor.Stats{StartedAt: started, Claimed: 1}

	// A panic must not kill the dashboard, but recovering alone is not enough:
	// the job still has to be reported and recorded. Run the work inside a
	// closure that converts a panic into an ordinary error, so the normal
	// failure path below runs for panics exactly as it does for errors.
	err := s.runJobSafely(queued)

	stats.Duration = time.Since(started)
	if err != nil {
		stats.Failed = 1
		s.logger.ErrorContext(s.workerCtx, "manual enrichment failed", "link_id", job.ID, "error", err)
	} else {
		stats.Completed = 1
		s.logger.InfoContext(s.workerCtx, "manual enrichment completed", "link_id", job.ID)
	}
	s.tracker.Record(stats, err)
}

type rejection struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

func writeProcessingResult(writer http.ResponseWriter, status int, accepted []int64, rejected []rejection) {
	if accepted == nil {
		accepted = []int64{}
	}
	if rejected == nil {
		rejected = []rejection{}
	}
	writeJSON(writer, status, map[string]any{
		"accepted": accepted,
		"rejected": rejected,
	})
}

func bookmarkQuery(request *http.Request) (cairn.BookmarkQuery, error) {
	values := request.URL.Query()
	limit := defaultPageSize
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageSize {
			return cairn.BookmarkQuery{}, errors.New("invalid limit")
		}
		limit = parsed
	}

	var beforeID int64
	if raw := values.Get("before_id"); raw != "" {
		parsed, err := positiveID(raw)
		if err != nil {
			return cairn.BookmarkQuery{}, err
		}
		beforeID = parsed
	}
	status := values.Get("status")
	if !validStatusFilter(status) {
		return cairn.BookmarkQuery{}, errors.New("invalid status")
	}
	search := strings.TrimSpace(values.Get("q"))
	if utf8.RuneCountInString(search) > maxSearchLength || len(strings.Fields(search)) > 10 {
		return cairn.BookmarkQuery{}, errors.New("search is too long")
	}
	query := cairn.BookmarkQuery{
		Limit: limit, BeforeID: beforeID, Status: status, Search: search,
		CurationStatus: values.Get("curation_status"), Topic: values.Get("topic"),
		Form: values.Get("form"), Use: values.Get("use"), Source: values.Get("source"), Since: values.Get("since"),
		SummaryOnly: values.Get("view") == "summary",
	}
	if view := values.Get("view"); view != "" && view != "summary" {
		return cairn.BookmarkQuery{}, errors.New("invalid view")
	}
	if query.CurationStatus != "" && query.CurationStatus != "all" && !taxonomy.ValidCurationStatus(query.CurationStatus) {
		return cairn.BookmarkQuery{}, errors.New("invalid curation status")
	}
	if query.Source != "" && query.Source != "x" && query.Source != "wechat" && query.Source != "other" {
		return cairn.BookmarkQuery{}, errors.New("invalid source")
	}
	for _, value := range []string{query.Topic, query.Form, query.Use} {
		if len(value) > 40 || strings.ContainsAny(value, " \t\r\n") {
			return cairn.BookmarkQuery{}, errors.New("invalid classification filter")
		}
	}
	if raw := values.Get("uncertain"); raw != "" {
		if raw != "true" {
			return cairn.BookmarkQuery{}, errors.New("invalid uncertainty filter")
		}
		query.Uncertain = true
	}
	if query.Since != "" {
		if _, err := time.Parse(time.RFC3339Nano, query.Since); err != nil {
			return cairn.BookmarkQuery{}, errors.New("invalid since filter")
		}
	}
	return query, nil
}

func uniqueIDs(raw []int64) ([]int64, error) {
	if len(raw) == 0 || len(raw) > maxManualBatch {
		return nil, fmt.Errorf("ids must contain 1 to %d items", maxManualBatch)
	}
	seen := make(map[int64]struct{}, len(raw))
	ids := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id < 1 {
			return nil, errors.New("ids must be positive")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func positiveID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("ID must be positive")
	}
	return id, nil
}

func validStatusFilter(status string) bool {
	switch status {
	case "", "all", "pending", "processing", "completed", "failed", "exhausted", "unsupported":
		return true
	default:
		return false
	}
}

func (s *Server) writeBackendError(writer http.ResponseWriter, operation string, id int64, err error) {
	s.logger.Error(operation, "link_id", id, "error", err)
	var apiErr *cairn.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "not_found":
			writeError(writer, http.StatusNotFound, apiErr.Code)
			return
		case "job_busy":
			writeError(writer, http.StatusConflict, apiErr.Code)
			return
		case "invalid_limit", "invalid_before_id", "invalid_status", "invalid_query", "invalid_curation":
			writeError(writer, http.StatusBadRequest, apiErr.Code)
			return
		}
		// An optimistic-concurrency conflict is actionable: the UI must show the
		// current revision and let the user re-apply, not a generic 502. The
		// server's revision is forwarded when it provided one (F04).
		if apiErr.IsConflict() {
			payload := map[string]any{"error": apiErr.Code}
			if apiErr.Revision != nil {
				payload["revision"] = *apiErr.Revision
			}
			writeJSON(writer, http.StatusConflict, payload)
			return
		}
		if apiErr.StatusCode == http.StatusConflict {
			writeError(writer, http.StatusConflict, apiErr.Code)
			return
		}
	}
	writeError(writer, http.StatusBadGateway, "backend_error")
}

func publicErrorCode(err error) string {
	var apiErr *cairn.APIError
	if errors.As(err, &apiErr) && (apiErr.Code == "not_found" || apiErr.Code == "job_busy") {
		return apiErr.Code
	}
	return "backend_error"
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"error": code})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
