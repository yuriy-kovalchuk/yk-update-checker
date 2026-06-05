// Package scheduler runs a scan on a fixed interval.
package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Runner executes a function on a fixed interval.
type Runner struct {
	interval time.Duration
	fn       func(ctx context.Context) error
}

// New creates a Runner that calls fn immediately and then on each interval tick.
func New(interval time.Duration, fn func(ctx context.Context) error) *Runner {
	return &Runner{interval: interval, fn: fn}
}

// Start runs the scheduler until ctx is cancelled.
// It fires immediately on start and then on every interval tick.
func (r *Runner) Start(ctx context.Context) {
	slog.Info("scheduler started", "interval", r.interval)
	if err := r.fn(ctx); err != nil {
		slog.Error("scheduled scan failed", "error", err)
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := r.fn(ctx); err != nil {
				slog.Error("scheduled scan failed", "error", err)
			}
		case <-ctx.Done():
			slog.Info("scheduler stopped")
			return
		}
	}
}
