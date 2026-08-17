package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

// stubService is a minimal scan.Service for dashboard handler tests.
type stubService struct {
	results    []scan.Result
	resultsErr error
	status     scan.Status
}

func (s *stubService) RunScan(context.Context) error { return nil }
func (s *stubService) StoreResults(context.Context, []scan.Result, time.Time, string, time.Duration) error {
	return nil
}
func (s *stubService) GetResults(context.Context) ([]scan.Result, error) {
	return s.results, s.resultsErr
}
func (s *stubService) GetStatus(context.Context) scan.Status { return s.status }
func (s *stubService) Trigger(context.Context) error         { return nil }
func (s *stubService) SetTrigger(trigger.Trigger)            {}
func (s *stubService) SetMeter(scan.MetricsCallback)         {}

var errTest = errors.New("test error")

func newTestMux(svc scan.Service) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux
}

func TestUIServesEmbeddedHTML(t *testing.T) {
	mux := newTestMux(&stubService{})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
	if !strings.Contains(w.Body.String(), "<title>Update Checker</title>") {
		t.Error("served body is not the embedded UI (missing expected title)")
	}
}

func TestFaviconServesSVG(t *testing.T) {
	mux := newTestMux(&stubService{})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/favicon.svg", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if !strings.Contains(w.Body.String(), "<svg") {
		t.Error("served body is not SVG")
	}
}

func TestResultsReturnsJSON(t *testing.T) {
	results := []scan.Result{{
		Source:          "homelab",
		Chart:           "app",
		Dependency:      "podinfo",
		CurrentVersion:  "1.0.0",
		LatestVersion:   "2.0.0",
		UpdateAvailable: true,
	}}
	mux := newTestMux(&stubService{results: results})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/results", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got []scan.Result
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Dependency != "podinfo" || !got[0].UpdateAvailable {
		t.Errorf("decoded results = %+v, want the stub result", got)
	}
}

func TestResultsEmptyIsJSONArray(t *testing.T) {
	// The UI relies on [] (not null) for an empty result set.
	mux := newTestMux(&stubService{results: []scan.Result{}})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/results", nil))

	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestResultsErrorReturns500(t *testing.T) {
	mux := newTestMux(&stubService{resultsErr: errTest})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/results", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestStatusReturnsJSON(t *testing.T) {
	scannedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc := &stubService{status: scan.Status{
		Scanning:         false,
		TriggerAvailable: true,
		LastScanAt:       &scannedAt,
		ResultCount:      7,
		Version:          "test",
	}}
	mux := newTestMux(svc)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got scan.Status
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.TriggerAvailable || got.ResultCount != 7 || got.Version != "test" {
		t.Errorf("decoded status = %+v, want the stub status", got)
	}
	if got.LastScanAt == nil || !got.LastScanAt.Equal(scannedAt) {
		t.Errorf("LastScanAt = %v, want %v", got.LastScanAt, scannedAt)
	}
}
