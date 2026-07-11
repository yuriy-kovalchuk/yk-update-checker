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
	StoreResults(ctx context.Context, results []Result, scannedAt time.Time) error
	// GetResults returns the latest scan results.
	GetResults(ctx context.Context) ([]Result, error)
	// GetStatus returns current scanning state.
	GetStatus(ctx context.Context) Status
	// Trigger initiates a scan via the configured trigger.
	Trigger(ctx context.Context) error
	// SetTrigger configures the trigger used by Trigger().
	SetTrigger(t trigger.Trigger)
}

type service struct {
	runner *Runner
	repo   Repository
	trig   trigger.Trigger

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

func (s *service) RunScan(ctx context.Context) error {
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
	return nil
}

func (s *service) setLastError(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *service) StoreResults(_ context.Context, results []Result, scannedAt time.Time) error {
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
