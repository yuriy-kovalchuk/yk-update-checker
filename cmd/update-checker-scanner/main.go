// update-checker-scanner runs a single scan and posts results to the API server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/api"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/db"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/scan"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/version"
)

func main() {
	var (
		configPath  = flag.String("config", "/etc/update-checker-scanner/config.yaml", "path to config file")
		apiURL      = flag.String("api-url", "http://localhost:8080", "API server URL")
		trigger     = flag.String("trigger", triggerDefault(), "scan trigger: manual, scheduled")
		gitCacheDir = flag.String("git-cache", "", "git cache directory (overrides config)")
		verbose     = flag.Bool("verbose", false, "enable debug logging")
	)
	flag.Parse()

	config.SetupLogger(*verbose)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	if len(cfg.Repos) == 0 {
		slog.Error("no repositories configured")
		os.Exit(1)
	}

	cacheDir := cfg.GitCacheDir
	if *gitCacheDir != "" {
		cacheDir = *gitCacheDir
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runScan(ctx, cfg, *apiURL, *trigger, cacheDir); err != nil {
		slog.Error("scan failed", "error", err)
		os.Exit(1)
	}
}

func runScan(ctx context.Context, cfg *config.Config, apiURL, trigger, cacheDir string) error {
	client := api.NewClient(apiURL)
	scope := version.ParseScope(cfg.UpdateType)
	repos := convertRepos(cfg.Repos)

	slog.Info("starting scan", "repos", len(repos), "scope", scope, "trigger", trigger)

	// Create scan record
	scanID, err := client.CreateScan(ctx, string(scope), trigger)
	if err != nil {
		return fmt.Errorf("create scan: %w", err)
	}
	slog.Info("scan record created", "scan_id", scanID)

	// Run scan
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart(), extractor.NewFluxCD()}
	}
	runner := scan.NewRunner(repos, newExtractors, scope, cfg.ParallelChecks, cacheDir)

	results, err := runner.Run(ctx)
	if err != nil {
		if ferr := client.FailScan(ctx, scanID, err.Error()); ferr != nil {
			slog.Error("fail scan request failed", "scan_id", scanID, "error", ferr)
		}
		return fmt.Errorf("scan: %w", err)
	}

	// Convert and store results
	dbResults := make([]db.Result, len(results))
	for i, r := range results {
		dbResults[i] = toDBResult(scanID, r)
	}

	if err := client.AddResults(ctx, scanID, dbResults); err != nil {
		if ferr := client.FailScan(ctx, scanID, err.Error()); ferr != nil {
			slog.Error("fail scan request failed", "scan_id", scanID, "error", ferr)
		}
		return fmt.Errorf("add results: %w", err)
	}

	if err := client.CompleteScan(ctx, scanID, len(results)); err != nil {
		return fmt.Errorf("complete scan: %w", err)
	}

	slog.Info("scan completed", "scan_id", scanID, "results", len(results))
	return nil
}

func convertRepos(repos []config.Repo) []scan.RepoTarget {
	targets := make([]scan.RepoTarget, len(repos))
	for i, r := range repos {
		targets[i] = scan.RepoTarget{
			Name: r.Name,
			URL:  r.URL,
			Path: r.Path,
			Auth: r.Auth,
		}
	}
	return targets
}

func triggerDefault() string {
	if t := os.Getenv("SCAN_TRIGGER"); t != "" {
		return t
	}
	return "scheduled"
}

func toDBResult(scanID int64, r scan.Result) db.Result {
	return db.Result{
		ScanID:          scanID,
		Source:          r.Source,
		Chart:           r.Chart,
		Dependency:      r.Dependency,
		Type:            r.Type,
		Protocol:        r.Protocol,
		CurrentVersion:  r.CurrentVersion,
		LatestVersion:   r.LatestVersion,
		Scope:           r.Scope,
		UpdateAvailable: r.UpdateAvailable,
		CheckedAt:       r.CheckedAt,
	}
}

