// Package api provides the HTTP API server that manages the database.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/constants"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/db"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/middleware"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/trigger"
)

// Server is the HTTP API server that owns the database.
type Server struct {
	db      *db.DB
	trigger *trigger.KubernetesTrigger
	port    string
}

// Config holds server configuration.
type Config struct {
	DB      *db.DB
	Trigger *trigger.KubernetesTrigger
	Port    string
}

// New creates a new API server.
func New(cfg *Config) *Server {
	return &Server{
		db:      cfg.DB,
		trigger: cfg.Trigger,
		port:    cfg.Port,
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Scanner endpoints
	mux.HandleFunc("POST /api/scans", s.createScan)
	mux.HandleFunc("POST /api/scans/{id}/results", s.addResults)
	mux.HandleFunc("PATCH /api/scans/{id}", s.updateScan)

	// Dashboard endpoints
	mux.HandleFunc("GET /api/scans", s.listScans)
	mux.HandleFunc("GET /api/results", s.getResults)
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("POST /api/trigger", s.triggerScan)

	// Health
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.health)

	limitedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, constants.MaxRequestBody)
		mux.ServeHTTP(w, r)
	})

	handler := middleware.Chain(limitedMux, middleware.Recovery, middleware.Headers, middleware.Logger)

	srv := &http.Server{
		Addr:              ":" + s.port,
		Handler:           handler,
		ReadHeaderTimeout: constants.ReadHeaderTimeout,
	}

	go func() {
		slog.Info("api server started", "addr", "http://localhost:"+s.port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
		}
	}()

	go func() {
		ticker := time.NewTicker(constants.DBStuckScanCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n, err := s.db.FailStuckScans(constants.DBStuckScanTimeout)
				if err != nil {
					slog.Error("fail stuck scans", "error", err)
				} else if n > 0 {
					slog.Warn("auto-failed stuck scans", "count", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down api server")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), constants.ShutdownTimeout)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}

// createScan creates a new scan record (called by scanner).
func (s *Server) createScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope   string `json:"scope"`
		Trigger string `json:"trigger"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	trig := db.ScanTriggerManual
	if req.Trigger == string(db.ScanTriggerScheduled) {
		trig = db.ScanTriggerScheduled
	}

	scanID, err := s.db.CreateScan(req.Scope, trig)
	if err != nil {
		slog.Error("create scan failed", "error", err)
		http.Error(w, "failed to create scan", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": scanID})
}

// addResults adds results to a scan (called by scanner).
func (s *Server) addResults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid scan ID", http.StatusBadRequest)
		return
	}

	var results []db.Result
	if err := json.NewDecoder(r.Body).Decode(&results); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	now := time.Now()
	for i := range results {
		results[i].ScanID = id
		results[i].CheckedAt = now
	}

	if err := s.db.InsertResults(results); err != nil {
		slog.Error("insert results failed", "error", err)
		http.Error(w, "failed to insert results", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int{"count": len(results)})
}

// updateScan updates scan status (called by scanner to complete/fail).
func (s *Server) updateScan(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid scan ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Status      string `json:"status"`
		ResultCount int    `json:"result_count"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Status {
	case string(db.ScanStatusCompleted):
		if err := s.db.CompleteScan(id, req.ResultCount); err != nil {
			slog.Error("complete scan failed", "error", err)
			http.Error(w, "failed to complete scan", http.StatusInternalServerError)
			return
		}
	case string(db.ScanStatusFailed):
		if err := s.db.FailScan(id, req.Error); err != nil {
			slog.Error("fail scan failed", "error", err)
			http.Error(w, "failed to mark scan as failed", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

// getResults returns results from the most recent completed scan.
func (s *Server) getResults(w http.ResponseWriter, _ *http.Request) {
	results, err := s.db.GetLatestResults()
	if err != nil {
		slog.Error("get results failed", "error", err)
		http.Error(w, "failed to get results", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// getStatus returns current status (scanning, last scan, trigger availability).
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	scanning, err := s.db.IsScanning()
	if err != nil {
		slog.Error("is scanning check failed", "error", err)
		http.Error(w, "failed to get status", http.StatusInternalServerError)
		return
	}
	triggerAvailable := s.trigger != nil && s.trigger.Available()

	resp := map[string]any{
		"scanning":          scanning,
		"trigger_available": triggerAvailable,
		"version":           config.Version,
	}

	if scan, err := s.db.LatestCompletedScan(); err == nil && scan != nil {
		if scan.CompletedAt != nil {
			resp["last_scan"] = scan.CompletedAt.UTC().Format(time.RFC3339)
		}
		resp["result_count"] = scan.ResultCount
	}

	writeJSON(w, http.StatusOK, resp)
}

// triggerScan creates a K8s Job to run a scan.
func (s *Server) triggerScan(w http.ResponseWriter, r *http.Request) {
	if s.trigger == nil || !s.trigger.Available() {
		http.Error(w, "trigger not available", http.StatusServiceUnavailable)
		return
	}

	jobName, err := s.trigger.Trigger(r.Context())
	if err != nil {
		slog.Error("trigger scan failed", "error", err)
		http.Error(w, "failed to trigger scan", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"job": jobName})
}

// listScans returns recent scan history (last 20).
func (s *Server) listScans(w http.ResponseWriter, _ *http.Request) {
	scans, err := s.db.ListScans(constants.DBListScansDefault)
	if err != nil {
		slog.Error("list scans failed", "error", err)
		http.Error(w, "failed to list scans", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, scans)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "error", err)
	}
}

