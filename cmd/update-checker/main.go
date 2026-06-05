// Command update-checker provides serve and scan subcommands for the yk-update-checker tool.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/api"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
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
	if len(os.Args) < 2 || os.Args[1] == "serve" {
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
	runner := buildRunner(cfg)
	svc := scan.NewService(runner, repo)

	// Prefer K8s trigger when a CronJob name is provided; fall back to inline.
	var trig trigger.Trigger
	if *cronJobName != "" {
		trig = trigger.NewKubernetesTrigger(*cronJobName)
	}
	if trig == nil || !trig.Available() {
		trig = trigger.NewInline(svc.RunScan)
	}
	svc.SetTrigger(trig)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start optional internal scheduler.
	if *interval > 0 {
		s := scheduler.New(*interval, svc.RunScan)
		go s.Start(ctx)
	}

	srv := api.New(*port)
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

	runner := buildRunner(cfg)
	results, err := runner.Run(ctx)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	if *serverURL != "" {
		return postResults(ctx, *serverURL, results)
	}

	// No server URL: print JSON to stdout.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildRunner(cfg *config.Config) *scan.Runner {
	scope := registry.ParseScope(cfg.UpdateType)
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{
			extractor.NewHelmChart(),
			extractor.NewFluxCD(),
		}
	}
	return scan.NewRunner(cfg.Repos, newExtractors, scope, cfg.ParallelChecks, cfg.GitCacheDir)
}

var scanClient = &http.Client{Timeout: 30 * time.Second}

func postResults(ctx context.Context, serverURL string, results []scan.Result) error {
	payload := struct {
		Results   []scan.Result `json:"results"`
		ScannedAt time.Time     `json:"scanned_at"`
	}{
		Results:   results,
		ScannedAt: time.Now(),
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
