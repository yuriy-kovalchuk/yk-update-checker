package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

type fakeTrigger struct {
	available bool
	running   bool
	err       error
}

func (f *fakeTrigger) Trigger(context.Context) error { return f.err }
func (f *fakeTrigger) Available() bool               { return f.available }
func (f *fakeTrigger) Running(context.Context) bool  { return f.running }

func TestRunScanKeepsPreviousResultsOnTotalFailure(t *testing.T) {
	repo := NewRepository()
	previous := []Result{{Source: "old", Dependency: "dep", CurrentVersion: "1.0.0"}}
	if err := repo.Save(previous, time.Now()); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner([]config.Repo{
		{Name: "broken", URL: filepath.Join(t.TempDir(), "missing")},
	})
	svc := NewService(runner, repo)

	if err := svc.RunScan(context.Background()); err == nil {
		t.Fatal("RunScan: want error when all repos fail, got nil")
	}

	results, _, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Source != "old" {
		t.Errorf("stored results = %v, want previous results preserved", results)
	}

	st := svc.GetStatus(context.Background())
	if st.LastError == "" {
		t.Error("Status.LastError empty, want failure recorded")
	}
}

func TestGetStatusReportsTriggerRunning(t *testing.T) {
	svc := NewService(nil, NewRepository())
	svc.SetTrigger(&fakeTrigger{available: true, running: true})

	st := svc.GetStatus(context.Background())
	if !st.Scanning {
		t.Error("Status.Scanning = false, want true while trigger reports a running scan")
	}
	if !st.TriggerAvailable {
		t.Error("Status.TriggerAvailable = false, want true")
	}
}

func TestStoreResultsRecordsMeterAndSaves(t *testing.T) {
	repo := NewRepository()
	svc := NewService(nil, repo)

	var gotStatus string
	var gotElapsed time.Duration
	svc.SetMeter(func(status string, elapsed time.Duration) {
		gotStatus = status
		gotElapsed = elapsed
	})

	results := []Result{{Source: "s", Dependency: "d", CurrentVersion: "1.0.0"}}
	if err := svc.StoreResults(context.Background(), results, time.Now(), "success", 2*time.Second); err != nil {
		t.Fatal(err)
	}

	if gotStatus != "success" {
		t.Errorf("meter status = %q, want %q", gotStatus, "success")
	}
	if gotElapsed != 2*time.Second {
		t.Errorf("meter elapsed = %v, want %v", gotElapsed, 2*time.Second)
	}

	stored, _, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Source != "s" {
		t.Errorf("stored results = %v, want results saved", stored)
	}
}

func TestStoreResultsFailureKeepsPreviousResults(t *testing.T) {
	repo := NewRepository()
	previous := []Result{{Source: "old", Dependency: "dep", CurrentVersion: "1.0.0"}}
	if err := repo.Save(previous, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, repo)

	var gotStatus string
	svc.SetMeter(func(status string, _ time.Duration) { gotStatus = status })

	if err := svc.StoreResults(context.Background(), nil, time.Now(), "failure", time.Second); err != nil {
		t.Fatal(err)
	}

	if gotStatus != "failure" {
		t.Errorf("meter status = %q, want %q", gotStatus, "failure")
	}

	stored, _, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Source != "old" {
		t.Errorf("stored results = %v, want previous results preserved on failure", stored)
	}
}

func TestStoreResultsDefaultsEmptyStatusToSuccess(t *testing.T) {
	repo := NewRepository()
	svc := NewService(nil, repo)

	var gotStatus string
	svc.SetMeter(func(status string, _ time.Duration) { gotStatus = status })

	results := []Result{{Source: "s", Dependency: "d", CurrentVersion: "1.0.0"}}
	if err := svc.StoreResults(context.Background(), results, time.Now(), "", 0); err != nil {
		t.Fatal(err)
	}

	if gotStatus != "success" {
		t.Errorf("meter status = %q, want %q (empty status defaults to success)", gotStatus, "success")
	}
	stored, _, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Errorf("stored results = %v, want results saved on default status", stored)
	}
}

func TestStoreResultsHandlerForwardsStatusAndDuration(t *testing.T) {
	repo := NewRepository()
	svc := NewService(nil, repo)

	var gotStatus string
	var gotElapsed time.Duration
	svc.SetMeter(func(status string, elapsed time.Duration) {
		gotStatus = status
		gotElapsed = elapsed
	})

	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)

	body := `{"results":[{"source":"s","dependency":"d","current_version":"1.0.0"}],"status":"success","duration_seconds":3.5}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/scan/results", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if gotStatus != "success" {
		t.Errorf("meter status = %q, want %q", gotStatus, "success")
	}
	if gotElapsed != 3500*time.Millisecond {
		t.Errorf("meter elapsed = %v, want %v", gotElapsed, 3500*time.Millisecond)
	}
}

func TestTriggerHandlerReturnsConflictWhenAlreadyRunning(t *testing.T) {
	svc := NewService(nil, NewRepository())
	svc.SetTrigger(&fakeTrigger{available: true, err: trigger.ErrAlreadyRunning})

	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/scan/trigger", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}
