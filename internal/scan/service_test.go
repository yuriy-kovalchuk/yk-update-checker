package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
