// Package dashboard serves the web UI and read-only API endpoints.
package dashboard

import (
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

//go:embed ui
var uiFS embed.FS

// Handler serves the dashboard UI and result/status endpoints.
type Handler struct {
	svc scan.Service
}

// NewHandler creates a new dashboard Handler.
func NewHandler(svc scan.Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes wires dashboard endpoints onto the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.ui)
	mux.HandleFunc("GET /api/results", h.results)
	mux.HandleFunc("GET /api/status", h.status)
}

func (h *Handler) ui(w http.ResponseWriter, _ *http.Request) {
	data, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "UI not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		slog.Error("serve UI write failed", "error", err)
	}
}

func (h *Handler) results(w http.ResponseWriter, r *http.Request) {
	results, err := h.svc.GetResults(r.Context())
	if err != nil {
		slog.Error("get results failed", "error", err)
		http.Error(w, "failed to get results", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.GetStatus(r.Context()))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "error", err)
	}
}
