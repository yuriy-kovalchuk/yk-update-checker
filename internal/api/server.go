// Package api provides the HTTP server that wires all features together.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/middleware"
)

const (
	// shutdownTimeout plus drainDelay must stay under the pod's terminationGracePeriodSeconds (30s by default).
	shutdownTimeout   = 20 * time.Second
	drainDelay        = 5 * time.Second
	readHeaderTimeout = 10 * time.Second

	// readTimeout bounds a whole request (body included), not just its headers: the body cap alone does not stop a client that dribbles bytes (S7).
	readTimeout = 30 * time.Second
	// writeTimeout covers the largest response — /api/results and the UI assets.
	writeTimeout = 30 * time.Second
	// idleTimeout closes keep-alive connections the dashboard never reuses; the UI polls every few seconds, so live polls are unaffected.
	idleTimeout = 2 * time.Minute
)

// Server is the HTTP server for the update-checker.
type Server struct {
	port string

	// draining is set once shutdown begins: /ready then answers 503 so the Service stops routing here while in-flight requests finish.
	draining   atomic.Bool
	drainDelay time.Duration
}

// New creates an HTTP server listening on the given port.
func New(port string) *Server {
	return &Server{port: port, drainDelay: drainDelay}
}

// Routes registers handlers on the server's mux.
type Routes interface{ RegisterRoutes(mux *http.ServeMux) }

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, routes Routes) error {
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux)

	// /health is liveness: the process serves.
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /ready", s.ready)

	handler := middleware.Chain(mux, middleware.Recovery, middleware.Headers, middleware.Logger)

	srv := newServer(s.port, handler)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server started", "addr", "http://localhost:"+s.port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		// A dead listener must kill the process, not leave it running headless.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	// Fail readiness first and give the endpoints controller time to take this pod out of the Service, then stop accepting connections.
	s.draining.Store(true)
	slog.Info("shutting down", "drain", s.drainDelay)
	time.Sleep(s.drainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newServer builds the HTTP server with every timeout set (S7).
func newServer(port string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"draining"}`))
		return
	}
	health(w, r)
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
