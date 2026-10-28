// Package dashboard serves the web UI, the read-only API and the scan trigger.
package dashboard

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/middleware"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/version"
)

//go:embed ui
var uiFS embed.FS

var (
	uiSub    = mustSub(uiFS, "ui")
	fontsSub = mustSub(uiFS, "ui/fonts")
)

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("dashboard: embedded " + dir + " is missing: " + err.Error())
	}
	return sub
}

// Handler serves what the latest report snapshot says and starts scans.
type Handler struct {
	Snapshot func() report.Snapshot
	Trigger  trigger.Trigger
	Metrics  http.Handler // mounted at /metrics when set
}

// RegisterRoutes wires the dashboard endpoints onto the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", serveUI("index.html"))
	mux.HandleFunc("GET /favicon.svg", serveUI("favicon.svg"))
	mux.Handle("GET /api/results", cached("no-store", http.HandlerFunc(h.results)))
	mux.Handle("GET /api/status", cached("no-store", http.HandlerFunc(h.status)))
	mux.HandleFunc("POST /api/scan/trigger", h.trigger)
	if h.Metrics != nil {
		mux.Handle("GET /metrics", h.Metrics)
	}
	mux.Handle("GET /assets/", cached("no-cache", http.StripPrefix("/assets/", http.FileServerFS(uiSub))))
	mux.Handle("GET /fonts/", cached("public, max-age=604800, immutable", http.StripPrefix("/fonts/", http.FileServerFS(fontsSub))))
}

func serveUI(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, uiSub, name) }
}

func cached(cacheControl string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cacheControl)
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) results(w http.ResponseWriter, _ *http.Request) {
	deps := h.Snapshot().Deps
	out := make([]report.DepStatus, len(deps))
	for i, d := range deps {
		out[i] = d.Status
	}
	middleware.WriteJSON(w, http.StatusOK, out)
}

// FailedRepo is a repository whose latest scan failed.
type FailedRepo struct {
	Repo    string       `json:"repo"`
	Kind    errkind.Kind `json:"kind"`
	Message string       `json:"message"`
}

// Status is what the UI polls.
type Status struct {
	Scanning         bool         `json:"scanning"`
	TriggerAvailable bool         `json:"trigger_available"`
	LastScanAt       *time.Time   `json:"last_scan_at,omitempty"`
	ResultCount      int          `json:"result_count"`
	LastError        string       `json:"last_error,omitempty"`
	LastErrorKind    errkind.Kind `json:"last_error_kind,omitempty"`
	RepoErrors       []FailedRepo `json:"repo_errors,omitempty"`
	Version          string       `json:"version"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	snap := h.Snapshot()
	st := Status{
		Scanning:         h.Trigger.Running(r.Context()),
		TriggerAvailable: h.Trigger.Available(),
		ResultCount:      len(snap.Deps),
		Version:          version.Version,
	}
	var msgs []string
	for _, rs := range snap.Repos {
		if t := rs.Status.LastScanTime.Time; st.LastScanAt == nil || t.After(*st.LastScanAt) {
			st.LastScanAt = &t
		}
		if e := rs.Status.Error; e != nil {
			st.RepoErrors = append(st.RepoErrors, FailedRepo{Repo: rs.Status.Repo, Kind: e.Kind, Message: e.Message})
			msgs = append(msgs, "repo "+rs.Status.Repo+": "+e.Message)
			if st.LastErrorKind == "" {
				st.LastErrorKind = e.Kind
			}
		}
	}
	st.LastError = strings.Join(msgs, "\n")
	middleware.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	if !h.Trigger.Available() {
		http.Error(w, "trigger not available", http.StatusServiceUnavailable)
		return
	}
	err := h.Trigger.Trigger(r.Context())
	switch {
	case errors.Is(err, trigger.ErrAlreadyRunning):
		middleware.WriteJSON(w, http.StatusConflict, map[string]string{"status": "already_running"})
	case err != nil:
		slog.Error("trigger scan failed", "error", err)
		http.Error(w, "failed to trigger scan", http.StatusInternalServerError)
	default:
		middleware.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
	}
}
