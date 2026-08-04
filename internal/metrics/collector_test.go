package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// testRepo implements scan.Repository for tests.
type testRepo struct {
	results   []scan.Result
	scannedAt time.Time
}

func (r *testRepo) Save(results []scan.Result, scannedAt time.Time) error {
	r.results = results
	r.scannedAt = scannedAt
	return nil
}

func (r *testRepo) Load() ([]scan.Result, *time.Time, error) {
	t := r.scannedAt
	return r.results, &t, nil
}

func TestResultCollector_NoResults(t *testing.T) {
	repo := &testRepo{}
	collector := NewResultCollector(repo)

	reg := mustNewRegistry(t)
	collector.Register(reg)

	count := testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 0 {
		t.Errorf("expected 0 metrics, got %d", count)
	}
}

func TestResultCollector_FiltersUpdateAvailableFalse(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "repo-1", Chart: "chart-a", Dependency: "dep-x", Type: "helm", Protocol: "https", Scope: "patch", CurrentVersion: "1.0.0", LatestVersion: "1.0.1", UpdateAvailable: false, CheckedAt: now},
			{Source: "repo-1", Chart: "chart-a", Dependency: "dep-y", Type: "helm", Protocol: "https", Scope: "minor", CurrentVersion: "1.0.0", LatestVersion: "1.1.0", UpdateAvailable: false, CheckedAt: now},
		},
		scannedAt: now,
	}

	reg := mustNewRegistry(t)
	NewResultCollector(repo).Register(reg)

	count := testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 0 {
		t.Errorf("expected 0 metrics (all UpdateAvailable=false), got %d", count)
	}
}

func TestResultCollector_EmitsOutdatedOnly(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "gitops", Chart: "nginx-ingress", Dependency: "ingress-nginx", Type: "helm", Protocol: "https", Scope: "major", CurrentVersion: "4.0.0", LatestVersion: "5.0.0", UpdateAvailable: true, CheckedAt: now},
			{Source: "gitops", Chart: "cert-manager", Dependency: "cert-manager", Type: "helm", Protocol: "https", Scope: "patch", CurrentVersion: "1.11.0", LatestVersion: "1.11.2", UpdateAvailable: true, CheckedAt: now},
			{Source: "gitops", Chart: "prometheus", Dependency: "kube-prometheus", Type: "helm", Protocol: "oci", Scope: "minor", CurrentVersion: "0.10.0", LatestVersion: "0.10.3", UpdateAvailable: false, CheckedAt: now},
		},
		scannedAt: now,
	}

	reg := mustNewRegistry(t)
	NewResultCollector(repo).Register(reg)

	count := testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 2 {
		t.Errorf("expected 2 metrics (only UpdateAvailable=true), got %d", count)
	}
}

func TestResultCollector_Labels(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "flux-repo", Chart: "release-1", Dependency: "common", Type: "fluxcd", Protocol: "oci", Scope: "major", CurrentVersion: "1.0.0", LatestVersion: "2.0.0", UpdateAvailable: true, CheckedAt: now},
		},
		scannedAt: now,
	}

	reg := mustNewRegistry(t)
	NewResultCollector(repo).Register(reg)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	for _, f := range families {
		if *f.Name != "update_checker_dependency_outdated_info" {
			continue
		}
		if len(f.Metric) != 1 {
			t.Fatalf("expected 1 metric, got %d", len(f.Metric))
		}
		m := f.Metric[0]

		labelMap := make(map[string]string)
		for _, l := range m.Label {
			labelMap[l.GetName()] = l.GetValue()
		}

		expectedLabels := map[string]string{
			"source":          "flux-repo",
			"chart":           "release-1",
			"dependency":      "common",
			"type":            "fluxcd",
			"protocol":        "oci",
			"scope":           "major",
			"current_version": "1.0.0",
			"latest_version":  "2.0.0",
		}

		for k, v := range expectedLabels {
			if labelMap[k] != v {
				t.Errorf("label %s: want %q, got %q", k, v, labelMap[k])
			}
		}

		if m.Gauge.GetValue() != 1 {
			t.Errorf("expected gauge value 1, got %f", m.Gauge.GetValue())
		}
	}
}

func TestResultCollector_DynamicState(t *testing.T) {
	now := time.Now()
	repo := &testRepo{}
	collector := NewResultCollector(repo)

	reg := mustNewRegistry(t)
	collector.Register(reg)

	// Initially empty.
	count := testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 0 {
		t.Errorf("phase 1: expected 0 metrics, got %d", count)
	}

	// Add results — collector reads live state, no re-registration needed.
	if err := repo.Save([]scan.Result{
		{Source: "a", Chart: "b", Dependency: "c", Type: "helm", Protocol: "https", Scope: "minor", CurrentVersion: "1.0", LatestVersion: "1.1", UpdateAvailable: true, CheckedAt: now},
	}, now); err != nil {
		t.Fatalf("save results: %v", err)
	}

	count = testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 1 {
		t.Errorf("phase 2: expected 1 metric, got %d", count)
	}

	// Clear results.
	if err := repo.Save(nil, now); err != nil {
		t.Fatalf("clear results: %v", err)
	}

	count = testutil.CollectAndCount(reg, "update_checker_dependency_outdated_info")
	if count != 0 {
		t.Errorf("phase 3: expected 0 metrics after clear, got %d", count)
	}
}

func mustNewRegistry(t *testing.T) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	t.Cleanup(func() { /* registry GC */ })
	return reg
}
