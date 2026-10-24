// Command update-checker runs the dashboard or a scan.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/api"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/dashboard"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/kube"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/memlimit"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
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
	if len(os.Args) < 2 {
		return errors.New("usage: update-checker dashboard|scan|version [flags]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "dashboard":
		return runDashboard(ctx, os.Args[2:])
	case "scan":
		return runScan(ctx, os.Args[2:])
	case "version":
		fmt.Printf("version=%s commit=%s build=%s go=%s\n", version.Version, version.Commit, version.BuildDate, version.GoVersion())
		return nil
	default:
		return fmt.Errorf("unknown command %q: use dashboard, scan or version", os.Args[1])
	}
}

func runDashboard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	port := fs.String("port", "8080", "HTTP port")
	mode := fs.String("mode", "local", "kubernetes: reports in CRDs, scans as Jobs; local: reports in --data, scans as child processes")
	cronJob := fs.String("cronjob", "", "kubernetes: scanner CronJob to create Jobs from")
	lease := fs.String("lease", "update-checker-leader", "kubernetes: Lease for leader election")
	cfgPath := fs.String("config", "config.yaml", "local: scanner config passed to each scan")
	data := fs.String("data", "data", "local: report directory")
	interval := fs.Duration("interval", 0, "local: scan every interval (0 = only on demand)")
	verbose := fs.Bool("verbose", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	setup(*verbose)

	var (
		backend report.Backend
		trig    trigger.Trigger
		leading func() bool
	)
	switch *mode {
	case "kubernetes":
		reports, env, err := kubeReports()
		if err != nil {
			return err
		}
		backend = reports
		trig = trigger.NewKubernetes(env.Client, env.Namespace, *cronJob)
		if leading, err = env.Leader(ctx, *lease); err != nil {
			return err
		}
	case "local":
		backend = report.Files{Dir: *data}
		trig = trigger.NewProcess(ctx, "scan", "--config", *cfgPath, "--store", "file", "--data", *data)
		if *interval > 0 {
			go report.Every(ctx, *interval, func() { _ = trig.Trigger(ctx) })
		}
	default:
		return fmt.Errorf("--mode must be kubernetes or local, got %q", *mode)
	}

	poller := report.NewPoller(backend)
	go poller.Run(ctx, 5*time.Second)
	return api.New(*port).Run(ctx, &dashboard.Handler{
		Snapshot: poller.Snapshot, Trigger: trig, Metrics: metrics.Handler(poller.Snapshot, leading)})
}

func runScan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	cfgPath := fs.String("config", "/etc/update-checker/config.yaml", "scanner config")
	store := fs.String("store", "none", "none: print JSON; kubernetes: write CRDs; file: write to --data")
	data := fs.String("data", "data", "file store directory")
	history := fs.Int("history", 10, "scan runs kept per repository")
	lock := fs.String("lock", "update-checker-scan", "kubernetes: Lease that allows one scan at a time")
	instance := fs.String("instance", "", "app.kubernetes.io/instance label on every report (the Helm release)")
	verbose := fs.Bool("verbose", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	setup(*verbose)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var writer *report.Writer
	switch *store {
	case "none":
	case "file":
		files := report.Files{Dir: *data}
		unlock, err := files.Lock()
		if err != nil {
			return err
		}
		defer unlock()
		writer = &report.Writer{Backend: files, History: *history, Instance: *instance}
	case "kubernetes":
		reports, env, err := kubeReports()
		if err != nil {
			return err
		}
		held, release, err := env.Lock(ctx, *lock)
		if err != nil {
			return err
		}
		defer release()
		ctx = held // losing the Lease stops the scan
		writer = &report.Writer{Backend: reports, History: *history, Instance: *instance}
	default:
		return fmt.Errorf("--store must be none, file or kubernetes, got %q", *store)
	}

	var mu sync.Mutex
	done := map[string]bool{}
	record := func(repo string, results []scan.Result, err error) error {
		mu.Lock()
		done[repo] = true
		mu.Unlock()
		return writer.Repo(ctx, repo, results, err)
	}
	var onRepo func(string, []scan.Result, error) error
	if writer != nil {
		names := make([]string, len(cfg.Repos))
		for i, r := range cfg.Repos {
			names[i] = r.Name
		}
		if err := writer.Prune(ctx, names); err != nil {
			return fmt.Errorf("prune reports of removed repositories: %w", err)
		}
		onRepo = record
	}
	runner, err := buildRunner(cfg, onRepo)
	if err != nil {
		return err
	}

	results, repoErrs, err := runner.Run(ctx)
	if err != nil {
		// A timeout is the scan's failure, worth a report; a cancellation (SIGTERM) just means "not scanned".
		if writer != nil && ctx.Err() == nil {
			for _, repo := range cfg.Repos {
				if !done[repo.Name] {
					err = errors.Join(err, record(repo.Name, nil, err))
				}
			}
		}
		return fmt.Errorf("scan: %w", err)
	}
	if writer == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	}
	if len(repoErrs) > 0 {
		return fmt.Errorf("scan finished with %d repo failure(s): %w", len(repoErrs), errors.Join(repoErrs...))
	}
	return nil
}

// kubeReports connects to the cluster and returns the custom-resource report store.
func kubeReports() (report.Kube, kube.Env, error) {
	env, err := kube.Connect()
	if err != nil {
		return report.Kube{}, env, err
	}
	dyn, err := dynamic.NewForConfig(env.Config)
	return report.Kube{Client: dyn, Namespace: env.Namespace}, env, err
}

func setup(verbose bool) {
	config.SetupLogger(verbose)
	if limit, ok := memlimit.Apply(); ok {
		slog.Info("go heap limit set from the container memory limit", "bytes", limit)
	}
}

func buildRunner(cfg *config.Config, onRepo func(string, []scan.Result, error) error) (*scan.Runner, error) {
	deadline, err := cfg.ScanDeadline()
	if err != nil {
		return nil, err
	}
	opts := []registry.CacheOption{registry.WithVerifyTags(cfg.VerifyImageTagsEnabled())}
	if cfg.RegistryAuthFile != "" {
		opts = append(opts, registry.WithRegistryAuthFile(cfg.RegistryAuthFile))
	}
	return scan.NewRunner(scan.RunnerOptions{
		Repos: cfg.Repos,
		NewExtractors: func() []extractor.Extractor {
			ex := []extractor.Extractor{extractor.NewHelmChart(), extractor.NewFluxCD()}
			if cfg.ImagesEnabled() {
				ex = append(ex, extractor.NewImages())
			}
			if cfg.DockerfilesEnabled() {
				ex = append(ex, extractor.NewDockerfile())
			}
			return ex
		},
		Scope:          registry.ParseScope(cfg.UpdateType),
		ParallelChecks: cfg.ParallelChecks,
		GitCacheDir:    cfg.GitCacheDir,
		Cache:          registry.NewIndexCache(opts...),
		ScanTimeout:    deadline,
		OnRepo:         onRepo,
	}), nil
}
