// update-checker-api is the API server that manages the database.
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
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/trigger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api server failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dbPath      = flag.String("db", "/data/update-checker-scanner.db", "path to SQLite database")
		port        = flag.String("port", "8080", "HTTP server port")
		verbose     = flag.Bool("verbose", false, "enable debug logging")
		cronJobName = flag.String("cronjob", "", "CronJob name for manual scan triggers")
	)
	flag.Parse()

	config.SetupLogger(*verbose)

	slog.Info("update-checker-api starting",
		"version", config.Version,
		"commit", config.Commit,
		"db", *dbPath,
		"port", *port,
	)

	// Open database
	database, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	// Set up K8s trigger (will be unavailable if not in cluster or no cronjob specified)
	trig := trigger.NewKubernetesTrigger(*cronJobName)

	// Create and run API server
	srv := api.New(&api.Config{
		DB:      database,
		Trigger: trig,
		Port:    *port,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return srv.Run(ctx)
}

