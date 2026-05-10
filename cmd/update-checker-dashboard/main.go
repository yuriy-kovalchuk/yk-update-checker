// update-checker-dashboard serves the web UI and proxies API requests.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/web"
)

func main() {
	var (
		apiURL  = flag.String("api-url", "http://localhost:8080", "API server URL")
		port    = flag.String("port", "8081", "HTTP server port")
		verbose = flag.Bool("verbose", false, "enable debug logging")
	)
	flag.Parse()

	config.SetupLogger(*verbose)

	slog.Info("update-checker-dashboard starting",
		"version", config.Version,
		"commit", config.Commit,
		"api_url", *apiURL,
		"port", *port,
	)

	apiTarget, err := url.Parse(*apiURL)
	if err != nil {
		slog.Error("invalid api-url", "error", err)
		os.Exit(1)
	}

	proxy := httputil.NewSingleHostReverseProxy(apiTarget)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy error", "error", err, "path", r.URL.Path)
		http.Error(w, "API server unavailable", http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		web.ServeUI(w, r)
	})
	mux.Handle("/api/", proxy)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})

	apiHealthURL := *apiURL + "/health"
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiHealthURL, nil)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"status":"unavailable"}`)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"status":"unavailable"}`)
			return
		}
		io.WriteString(w, `{"status":"ok"}`)
	})

	srv := &http.Server{Addr: ":" + *port, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("dashboard server started", "addr", "http://localhost:"+*port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}
}

