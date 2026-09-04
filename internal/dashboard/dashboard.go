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
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

const (
	defaultPageSize  = 20
	maxPageSize      = 60
	maxManualBatch   = 10
	manualQueueDepth = 100
	maxSearchLength  = 200
	maxActionBody    = 4 << 10
)

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

// Backend provides the internal Cloudflare data plane used by the dashboard.
type Backend interface {
	ListBookmarks(context.Context, cairn.BookmarkQuery) (cairn.BookmarkPage, error)
	GetBookmark(context.Context, int64) (cairn.BookmarkDetail, error)
	GetImage(context.Context, string) (*http.Response, error)
	ClaimByID(context.Context, int64) (*cairn.Job, error)
}

// JobProcessor handles a job after the Worker has granted its lease.
type JobProcessor interface {
	Process(context.Context, *cairn.Job) error
}

// Server owns the management HTTP surface and bounded manual work queue.
type Server struct {
	ctx       context.Context
	tracker   *health.Tracker
	backend   Backend
	processor JobProcessor
	logger    *slog.Logger
	jobs      chan *cairn.Job
	enqueueMu sync.Mutex
}

// New creates a dashboard and starts bounded manual processing workers.
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
	server := &Server{
		ctx:       ctx,
		tracker:   tracker,
		backend:   backend,
		processor: jobProcessor,
		logger:    logger,
		jobs:      make(chan *cairn.Job, manualQueueDepth),
	}
	for range workerCount {
		go server.runWorker()
	}
	return server
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

	healthHandler := s.tracker.Handler()
	mux.Handle("GET /healthz", healthHandler)
	mux.Handle("GET /readyz", healthHandler)
	mux.Handle("GET /status", healthHandler)

	mux.HandleFunc("GET /api/bookmarks", s.listBookmarks)
	mux.HandleFunc("GET /api/bookmarks/{id}", s.getBookmark)
	mux.HandleFunc("GET /api/images/{key...}", s.getImage)
	mux.HandleFunc("POST /api/bookmarks/process", s.processBookmarks)
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
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	if _, err := io.Copy(writer, response.Body); err != nil {
		s.logger.WarnContext(request.Context(), "stream image response", "error", err)
	}
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

	type rejection struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	accepted := make([]int64, 0, len(ids))
	rejected := make([]rejection, 0)

	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if len(s.jobs)+len(ids) > cap(s.jobs) {
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
		s.jobs <- job
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

func (s *Server) runWorker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.jobs:
			started := time.Now().UTC()
			stats := processor.Stats{StartedAt: started, Claimed: 1}
			err := s.processor.Process(s.ctx, job)
			stats.Duration = time.Since(started)
			if err != nil {
				stats.Failed = 1
				s.logger.ErrorContext(s.ctx, "manual enrichment failed", "link_id", job.ID, "error", err)
			} else {
				stats.Completed = 1
				s.logger.InfoContext(s.ctx, "manual enrichment completed", "link_id", job.ID)
			}
			s.tracker.Record(stats, err)
		}
	}
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
	if len(search) > maxSearchLength {
		return cairn.BookmarkQuery{}, errors.New("search is too long")
	}
	return cairn.BookmarkQuery{Limit: limit, BeforeID: beforeID, Status: status, Search: search}, nil
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
		case "invalid_limit", "invalid_before_id", "invalid_status", "invalid_query":
			writeError(writer, http.StatusBadRequest, apiErr.Code)
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
