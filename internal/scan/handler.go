// Package scan implements the scan domain: running dependency checks and storing results.
package scan

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

// Handler handles HTTP endpoints for scan operations.
type Handler struct {
	svc Service
}

// NewHandler creates a new scan Handler.
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes wires scan endpoints onto the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/scan/trigger", h.trigger)
	mux.HandleFunc("POST /api/scan/results", h.storeResults)
}

// trigger initiates a scan (background goroutine or K8s Job depending on deployment).
func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Trigger(r.Context()); err != nil {
		var unavail *ErrTriggerUnavailable
		switch {
		case errors.As(err, &unavail):
			http.Error(w, "trigger not available", http.StatusServiceUnavailable)
		case errors.Is(err, trigger.ErrAlreadyRunning):
			writeJSON(w, http.StatusConflict, map[string]string{"status": "already_running"})
		default:
			slog.Error("trigger scan failed", "error", err)
			http.Error(w, "failed to trigger scan", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
}

// storeResults accepts results pushed by an external scanner (K8s CronJob mode).
func (h *Handler) storeResults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Results   []Result  `json:"results"`
		ScannedAt time.Time `json:"scanned_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ScannedAt.IsZero() {
		req.ScannedAt = time.Now()
	}
	if err := h.svc.StoreResults(r.Context(), req.Results, req.ScannedAt); err != nil {
		slog.Error("store results failed", "error", err)
		http.Error(w, "failed to store results", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"count": len(req.Results)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "error", err)
	}
}
