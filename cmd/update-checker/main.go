// Command update-checker provides serve and scan subcommands for the yk-update-checker tool.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/api"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scheduler"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Default to serve when no subcommand is given, including bare flags
	// like `update-checker -port 9090`.
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		return runServe()
	}
	switch os.Args[1] {
	case "serve":
		return runServe()
	case "scan":
		return runScan()
	case "version":
		fmt.Printf("version=%s commit=%s build=%s go=%s\n",
			version.Version, version.Commit, version.BuildDate, version.GoVersion())
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q — use: serve, scan, version", os.Args[1])
	}
}

// ── serve ─────────────────────────────────────────────────────────────────────

func runServe() error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "/etc/update-checker/config.yaml", "path to config file")
	port := fs.String("port", "8080", "HTTP server port")
	interval := fs.Duration("interval", 0, "scan interval (e.g. 6h); 0 = no automatic scanning")
	cronJobName := fs.String("cronjob", "", "K8s CronJob name for manual scan triggers")
	verbose := fs.Bool("verbose", false, "enable debug logging")

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	config.SetupLogger(*verbose)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	repo := scan.NewRepository()
	ttl, err := cfg.RegistryCacheDuration()
	if err != nil {
		return err
	}
	// One cache for the process lifetime: scans closer together than the TTL
	// reuse recently fetched registry data instead of re-downloading it.
	runner := buildRunner(cfg, registry.NewIndexCache(ttl))
	svc := scan.NewService(runner, repo)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Prefer K8s trigger when a CronJob name is provided; fall back to inline.
	var trig trigger.Trigger
	if *cronJobName != "" {
		trig = trigger.NewKubernetesTrigger(*cronJobName)
	}
	if trig == nil || !trig.Available() {
		trig = trigger.NewInline(ctx, svc.RunScan)
	}
	svc.SetTrigger(trig)

	// Start optional internal scheduler.
	if *interval > 0 {
		s := scheduler.New(*interval, svc.RunScan)
		go s.Start(ctx)
	}

	// Register Prometheus metrics only when running inside Kubernetes.
	srv := api.New(*port)
	if metrics.InCluster() {
		exp := metrics.NewExporter(repo)
		svc.SetMeter(exp.Gauge().Record) // record scan success/failure + duration
		srv.SetHandler(exp.Handler())    // mount /metrics
	}
	return srv.Run(ctx, svc)
}

// ── scan ──────────────────────────────────────────────────────────────────────

func runScan() error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	configPath := fs.String("config", "/etc/update-checker/config.yaml", "path to config file")
	serverURL := fs.String("server-url", "", "dashboard URL to POST results to (e.g. http://update-checker-svc:8080)")
	verbose := fs.Bool("verbose", false, "enable debug logging")

	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	config.SetupLogger(*verbose)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	// One-shot process: the cache only lives for this run, so no TTL.
	runner := buildRunner(cfg, registry.NewIndexCache(0))
	results, repoErrs, err := runner.Run(ctx)
	if err != nil {
		if *serverURL != "" {
			// Best-effort: let the server record the failed run even though
			// there are no results to report. Don't let this mask the real error.
			if perr := postResults(ctx, *serverURL, nil, "failure", time.Since(start)); perr != nil {
				slog.Error("failed to report scan failure to server", "error", perr)
			}
		}
		return fmt.Errorf("scan: %w", err)
	}

	if *serverURL != "" {
		if err := postResults(ctx, *serverURL, results, "success", time.Since(start)); err != nil {
			return err
		}
	} else {
		// No server URL: print JSON to stdout.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	}

	// Report partial repo failures after delivering what was scanned, so the
	// Job exits non-zero and the failure is visible.
	if len(repoErrs) > 0 {
		return fmt.Errorf("scan finished with %d repo failure(s): %w", len(repoErrs), errors.Join(repoErrs...))
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildRunner(cfg *config.Config, cache *registry.IndexCache) *scan.Runner {
	scope := registry.ParseScope(cfg.UpdateType)
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{
			extractor.NewHelmChart(),
			extractor.NewFluxCD(),
		}
	}
	return scan.NewRunner(cfg.Repos, newExtractors, scope, cfg.ParallelChecks, cfg.GitCacheDir, cache)
}

var scanClient = &http.Client{Timeout: 30 * time.Second}

func postResults(ctx context.Context, serverURL string, results []scan.Result, status string, elapsed time.Duration) error {
	payload := struct {
		Results         []scan.Result `json:"results"`
		ScannedAt       time.Time     `json:"scanned_at"`
		Status          string        `json:"status"`
		DurationSeconds float64       `json:"duration_seconds"`
	}{
		Results:         results,
		ScannedAt:       time.Now(),
		Status:          status,
		DurationSeconds: elapsed.Seconds(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/scan/results", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := scanClient.Do(req)
	if err != nil {
		return fmt.Errorf("post results to %s: %w", serverURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	fmt.Printf("posted %d results to %s\n", len(results), serverURL)
	return nil
}
