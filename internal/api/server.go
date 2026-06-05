// Package api provides the HTTP server that wires all features together.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/dashboard"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/middleware"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

const (
	maxRequestBody    = 4 << 20 // 4 MB
	shutdownTimeout   = 30 * time.Second
	readHeaderTimeout = 10 * time.Second
)

// Server is the HTTP server for the update-checker.
type Server struct {
	port string
}

// New creates an HTTP server listening on the given port.
func New(port string) *Server {
	return &Server{port: port}
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, svc scan.Service) error {
	mux := http.NewServeMux()

	dashboard.NewHandler(svc).RegisterRoutes(mux)
	scan.NewHandler(svc).RegisterRoutes(mux)

	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /ready", health)

	limitedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		mux.ServeHTTP(w, r)
	})

	handler := middleware.Chain(limitedMux, middleware.Recovery, middleware.Headers, middleware.Logger)

	srv := &http.Server{
		Addr:              ":" + s.port,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() {
		slog.Info("server started", "addr", "http://localhost:"+s.port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
