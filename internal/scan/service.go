package scan

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/version"
)

// Service orchestrates scan execution and result storage.
type Service interface {
	// RunScan executes a full scan in-process and stores the results.
	RunScan(ctx context.Context) error
	// StoreResults stores results pushed by an external scanner (K8s CronJob mode).
	// status and elapsed mirror what RunScan reports to the meter locally; status
	// "failure" leaves previously stored results untouched.
	StoreResults(ctx context.Context, results []Result, scannedAt time.Time, status string, elapsed time.Duration) error
	// GetResults returns the latest scan results.
	GetResults(ctx context.Context) ([]Result, error)
	// GetStatus returns current scanning state.
	GetStatus(ctx context.Context) Status
	// Trigger initiates a scan via the configured trigger.
	Trigger(ctx context.Context) error
	// SetTrigger configures the trigger used by Trigger().
	SetTrigger(t trigger.Trigger)
	// SetMeter configures an optional metrics callback invoked after each RunScan.
	SetMeter(m MetricsCallback)
}

// MetricsCallback is called after a scan completes with its elapsed duration.
// The callback receives status="success" or "failure".  It is optional (nil = no-op).
type MetricsCallback func(status string, elapsed time.Duration)

type service struct {
	runner *Runner
	repo   Repository
	trig   trigger.Trigger
	meter  MetricsCallback

	mu       sync.Mutex
	scanning bool
	lastErr  string
}

// NewService creates a new scan Service.
func NewService(runner *Runner, repo Repository) Service {
	return &service{runner: runner, repo: repo}
}

func (s *service) SetTrigger(t trigger.Trigger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trig = t
}

// SetMeter configures a metrics callback that is invoked after every RunScan.
func (s *service) SetMeter(m MetricsCallback) {
	s.meter = m
}

func (s *service) RunScan(ctx context.Context) error {
	start := time.Now()

	s.mu.Lock()
	if s.scanning {
		s.mu.Unlock()
		slog.Info("scan already in progress, skipping")
		return nil
	}
	s.scanning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.scanning = false
		s.mu.Unlock()
	}()

	slog.Info("scan started")
	results, repoErrs, err := s.runner.Run(ctx)
	if err != nil {
		// Nothing was scanned (cancelled or all repos failed): keep the
		// previous results instead of overwriting them with an empty set.
		s.setLastError(err.Error())
		if s.meter != nil {
			s.meter("failure", time.Since(start))
		}
		return err
	}

	lastErr := ""
	if len(repoErrs) > 0 {
		lastErr = errors.Join(repoErrs...).Error()
		slog.Warn("scan completed with repo failures", "failed", len(repoErrs))
	}
	s.setLastError(lastErr)

	if err := s.repo.Save(results, time.Now()); err != nil {
		return err
	}
	slog.Info("scan completed", "results", len(results))
	if s.meter != nil {
		s.meter("success", time.Since(start))
	}
	return nil
}

func (s *service) setLastError(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *service) StoreResults(_ context.Context, results []Result, scannedAt time.Time, status string, elapsed time.Duration) error {
	if status == "" {
		status = "success"
	}
	if s.meter != nil {
		s.meter(status, elapsed)
	}
	if status == "failure" {
		// Mirrors RunScan: keep previously stored results instead of
		// overwriting them with whatever the failed scanner Job sent.
		return nil
	}
	return s.repo.Save(results, scannedAt)
}

func (s *service) GetResults(_ context.Context) ([]Result, error) {
	results, _, err := s.repo.Load()
	return results, err
}

func (s *service) GetStatus(ctx context.Context) Status {
	s.mu.Lock()
	scanning := s.scanning
	lastErr := s.lastErr
	trig := s.trig
	s.mu.Unlock()

	results, scannedAt, _ := s.repo.Load()

	trigAvailable := false
	if trig != nil {
		trigAvailable = trig.Available()
		// In K8s CronJob mode the scan runs in a separate Job pod; ask the
		// trigger so the dashboard shows "scanning" while that Job is active.
		if !scanning {
			scanning = trig.Running(ctx)
		}
	}

	return Status{
		Scanning:         scanning,
		TriggerAvailable: trigAvailable,
		LastScanAt:       scannedAt,
		ResultCount:      len(results),
		LastError:        lastErr,
		Version:          version.Version,
	}
}

func (s *service) Trigger(ctx context.Context) error {
	s.mu.Lock()
	trig := s.trig
	s.mu.Unlock()

	if trig == nil || !trig.Available() {
		return &ErrTriggerUnavailable{}
	}
	return trig.Trigger(ctx)
}

// ErrTriggerUnavailable is returned when no trigger is configured or available.
type ErrTriggerUnavailable struct{}

func (e *ErrTriggerUnavailable) Error() string { return "scan trigger not available" }
