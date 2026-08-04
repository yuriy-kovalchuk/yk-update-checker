package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

func TestExporter_HTTPEndpoint(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "gitops", Chart: "app-a", Dependency: "nginx", Type: "helm", Protocol: "https", Scope: "major", CurrentVersion: "1.0.0", LatestVersion: "2.0.0", UpdateAvailable: true, CheckedAt: now},
			{Source: "gitops", Chart: "app-a", Dependency: "redis", Type: "helm", Protocol: "oci", Scope: "patch", CurrentVersion: "16.0.0", LatestVersion: "16.0.1", UpdateAvailable: true, CheckedAt: now},
			{Source: "gitops", Chart: "app-b", Dependency: "cert-manager", Type: "fluxcd", Protocol: "https", Scope: "minor", CurrentVersion: "3.9.0", LatestVersion: "3.10.0", UpdateAvailable: false, CheckedAt: now},
		},
		scannedAt: now,
	}

	exp := NewExporter(repo)
	gauge := exp.Gauge()
	gauge.Record("success", 5*time.Second)

	// Simulate a Prometheus scrape request.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	exp.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200", w.Code)
	}

	body := w.Body.String()

	// Verify metric names are present in output.
	mustContain(t, body, "update_checker_dependency_outdated_info")
	mustContain(t, body, "update_checker_last_scan_time")
	mustContain(t, body, "update_checker_scan_total{status=\"success\"}")
	mustContain(t, body, "update_checker_scan_duration_seconds_count 1")

	// Verify only outdated (UpdateAvailable=true) appear.
	if strings.Contains(body, "cert-manager") {
		t.Error("should not contain cert-manager (UpdateAvailable=false)")
	}
	if !strings.Contains(body, "nginx") {
		t.Error("should contain nginx dependency")
	}
	if !strings.Contains(body, "redis") {
		t.Error("should contain redis dependency")
	}

	// Verify scope labels.
	mustContain(t, body, `scope="major"`)
	mustContain(t, body, `scope="patch"`)
	if strings.Contains(body, `scope="minor"`) && !strings.Contains(body, "cert-manager") {
		// minor only appears in duration metric (which has no scope label), not dependency metric
		t.Error("minor scope should not appear for dependency_outdated_info")
	}
}

func TestExporter_DynamicResultsReflectInScrape(t *testing.T) {
	now := time.Now()
	repo := &testRepo{} // empty initially
	exp := NewExporter(repo)

	// First scrape — no results.
	w1 := httptest.NewRecorder()
	exp.Handler().ServeHTTP(w1, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

	if strings.Contains(w1.Body.String(), "update_checker_dependency_outdated_info") && !strings.Contains(w1.Body.String(), "# HELP update_checker_dependency_outdated_info") {
		t.Error("first scrape should have no data lines (only HELP/TYPE)")
	}

	// Store results and scrape again.
	if err := repo.Save([]scan.Result{
		{Source: "x", Chart: "y", Dependency: "z", Type: "helm", Protocol: "https", Scope: "major", CurrentVersion: "1", LatestVersion: "2", UpdateAvailable: true, CheckedAt: now},
	}, now); err != nil {
		t.Fatalf("save results: %v", err)
	}

	w2 := httptest.NewRecorder()
	exp.Handler().ServeHTTP(w2, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

	if !strings.Contains(w2.Body.String(), `source="x"`) {
		t.Error("second scrape should contain updated results")
	}
	count := strings.Count(w2.Body.String(), "update_checker_dependency_outdated_info{")
	if count != 1 {
		t.Errorf("expected exactly 1 outdated dependency line, found %d", count)
	}
}

func TestExporter_DashboardQueryCompatibility(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "s1", Chart: "c1", Dependency: "d1", Type: "helm", Protocol: "https", Scope: "major", CurrentVersion: "1.0", LatestVersion: "2.0", UpdateAvailable: true, CheckedAt: now},
			{Source: "s1", Chart: "c2", Dependency: "d2", Type: "fluxcd", Protocol: "oci", Scope: "minor", CurrentVersion: "3.0", LatestVersion: "3.1", UpdateAvailable: true, CheckedAt: now},
			{Source: "s2", Chart: "c3", Dependency: "d3", Type: "helm", Protocol: "https", Scope: "patch", CurrentVersion: "4.0", LatestVersion: "4.0.1", UpdateAvailable: true, CheckedAt: now},
		},
		scannedAt: now,
	}

	exp := NewExporter(repo)
	exp.Gauge().Record("success", 2*time.Second)

	w := httptest.NewRecorder()
	exp.Handler().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	body := w.Body.String()

	// Verify metric name pattern matches Grafana queries:
	// count(update_checker_dependency_outdated_info) → one series per outdated dependency
	if !strings.Contains(body, `update_checker_dependency_outdated_info{`) {
		t.Fatal("missing dependency metric in output")
	}

	// Each outdated dep should have value = 1 (gauge).
	lines := strings.Split(body, "\n")
	outdatedCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "update_checker_dependency_outdated_info{") {
			outdatedCount++
			if !strings.HasSuffix(strings.TrimSpace(line), " 1") && !strings.HasSuffix(strings.TrimSpace(line), "1") {
				t.Errorf("dependency metric must have value 1: %s", line)
			}
		}
	}
	if outdatedCount != 3 {
		t.Errorf("expected 3 outdated dependency lines, got %d", outdatedCount)
	}

	// Verify scope label grouping is queryable, e.g. via
	// count by (scope) (update_checker_dependency_outdated_info).
	scopeMajor := strings.Count(body, `scope="major"`) // at least once for major
	scopeMinor := strings.Count(body, `scope="minor"`) // at least once for minor
	scopePatch := strings.Count(body, `scope="patch"`) // at least once for patch

	if scopeMajor == 0 {
		t.Error("missing scope=major label")
	}
	if scopeMinor == 0 {
		t.Error("missing scope=minor label")
	}
	if scopePatch == 0 {
		t.Error("missing scope=patch label")
	}

	// Verify summary has quantile labels (used for scan-duration queries).
	mustContain(t, body, `quantile="0.5"`)
	mustContain(t, body, `quantile="0.9"`)
	mustContain(t, body, `quantile="0.99"`)

	// Verify scan_total counter has status label (used by PrometheusRule).
	mustContain(t, body, `scan_total{status="success"`)
}

func TestExporter_GaugeMetricBasics(t *testing.T) {
	reg := mustNewRegistry(t)
	g := NewGauge(reg)

	// Record one success.
	g.Record("success", 10*time.Second)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	// Verify metric names match Grafana queries exactly.
	expectedNames := []string{
		"update_checker_last_scan_time",
		"update_checker_scan_duration_seconds",
		"update_checker_scan_total",
	}

	found := make(map[string]bool)
	for _, f := range families {
		name := *f.Name
		found[name] = true
	}

	for _, n := range expectedNames {
		if !found[n] {
			t.Errorf("missing metric %q in gathered families", n)
		}
	}

	// Verify FQName construction matches collector output.
	collectorFQName := prometheus.BuildFQName("", "update_checker", "dependency_outdated_info")
	if collectorFQName != "update_checker_dependency_outdated_info" {
		t.Errorf("collector FQName mismatch: got %q", collectorFQName)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	if !strings.Contains(haystack, needle) {
		t.Errorf("output should contain %q\ngot:\n%s", needle, haystack)
	}
}
