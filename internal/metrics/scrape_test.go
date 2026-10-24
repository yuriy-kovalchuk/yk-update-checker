package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
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

	exp := Handler(repo.snap, nil)

	// Simulate a Prometheus scrape request.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	exp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200", w.Code)
	}

	body := w.Body.String()

	// Verify metric names are present in output.
	mustContain(t, body, "update_checker_dependency_outdated_info")
	mustContain(t, body, "update_checker_last_scan_time")

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
	exp := Handler(repo.snap, nil)

	// First scrape — no results.
	w1 := httptest.NewRecorder()
	exp.ServeHTTP(w1, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

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
	exp.ServeHTTP(w2, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

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

	exp := Handler(repo.snap, nil)

	w := httptest.NewRecorder()
	exp.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	body := w.Body.String()

	// Verify metric name pattern matches Grafana queries:count(update_checker_dependency_outdated_info) → one series per outdated dependency
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

	// Verify scope label grouping is queryable, e.g. via count by (scope) (update_checker_dependency_outdated_info).
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

}

// TestExporter_CollidingLabelSetsStillScrape reproduces two results colliding on every label.
func TestExporter_CollidingLabelSetsStillScrape(t *testing.T) {
	now := time.Now()
	pinned := func(file string) scan.Result {
		return scan.Result{
			Source: "lab", Chart: "Dockerfile/build", Dependency: "alpine",
			Type: "dockerfile", Protocol: "image", Scope: "minor",
			CurrentVersion: "3.19", LatestVersion: "3.20",
			Path: file, UpdateAvailable: true, CheckedAt: now,
		}
	}
	repo := &testRepo{
		results:   []scan.Result{pinned("apps/api/Dockerfile"), pinned("apps/api/Dockerfile")},
		scannedAt: now,
	}

	exp := Handler(repo.snap, nil)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	exp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("scrape returned %d, want 200; body:\n%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if n := strings.Count(body, "update_checker_dependency_outdated_info{"); n != 1 {
		t.Errorf("series for the colliding label set = %d, want exactly 1\n%s", n, body)
	}
	// The rest of the endpoint has to survive the collision.
	mustContain(t, body, "update_checker_last_scan_time")
	mustContain(t, body, "update_checker_last_scan_time")
}

func mustContain(t *testing.T, haystack, needle string) {
	if !strings.Contains(haystack, needle) {
		t.Errorf("output should contain %q\ngot:\n%s", needle, haystack)
	}
}

// scrape serves /metrics from the exporter and returns the body, failing the test if the scrape itself failed.
func scrape(t *testing.T, exp http.Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	exp.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scrape returned %d, want 200; body:\n%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// TestExporter_KindPathAndTagKindLabels: a scrape says what kind of thing is outdated, where, and what its tag looks like.
func TestExporter_KindPathAndTagKindLabels(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "lab", Chart: "frontend", Dependency: "nginx", Type: "image", Kind: "Deployment", Protocol: "image", Path: "k8s/deploy.yaml", TagKind: "calver", Scope: "minor", CurrentVersion: "2024.01", LatestVersion: "2024.06", UpdateAvailable: true, CheckedAt: now},
			{Source: "lab", Chart: "release", Dependency: "cert-manager", Type: "fluxcd", Kind: "HelmRelease", Protocol: "oci", Path: "flux/apps/cert-manager/helmrelease.yaml", TagKind: "semver", Scope: "patch", CurrentVersion: "1.14.0", LatestVersion: "1.14.7", UpdateAvailable: true, CheckedAt: now},
		},
		scannedAt: now,
	}

	body := scrape(t, Handler(repo.snap, nil))
	for _, want := range []string{
		// Labels appear in the exposition format sorted by name.
		`update_checker_dependency_outdated_info{chart="frontend",current_version="2024.01",dependency="nginx",kind="Deployment",latest_version="2024.06",path="k8s/deploy.yaml",protocol="image",scope="minor",source="lab",tag_kind="calver",type="image"} 1`,
		`kind="HelmRelease"`,
		`path="flux/apps/cert-manager/helmrelease.yaml"`,
		`tag_kind="semver"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape is missing %q\n%s", want, body)
		}
	}
}

// TestExporter_ScannedCountsWithoutUpdates: coverage is unanswerable from a presence-only metric, so scanned carries the count.
func TestExporter_ScannedCountsWithoutUpdates(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "lab", Chart: "c", Dependency: "old", Type: "helm", Protocol: "https", Scope: "minor", CurrentVersion: "1.0", LatestVersion: "2.0", UpdateAvailable: true, CheckedAt: now},
			{Source: "lab", Chart: "c", Dependency: "fresh", Type: "helm", Protocol: "https", CurrentVersion: "2.0", LatestVersion: "2.0", CheckedAt: now},
			{Source: "lab", Chart: "w", Dependency: "nginx", Type: "image", Kind: "Deployment", Protocol: "image", CurrentVersion: "1.27", LatestVersion: "1.27", CheckedAt: now},
			{Source: "other", Chart: "c", Dependency: "bot", Type: "fluxcd", Kind: "HelmRelease", Protocol: "oci", CurrentVersion: "a9c31f2", CheckedAt: now},
		},
		scannedAt: now,
	}

	body := scrape(t, Handler(repo.snap, nil))
	for _, want := range []string{
		`update_checker_dependencies_scanned{source="lab",type="helm"} 2`,
		`update_checker_dependencies_scanned{source="lab",type="image"} 1`,
		`update_checker_dependencies_scanned{source="other",type="fluxcd"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape is missing %q\n%s", want, body)
		}
	}
	if n := strings.Count(body, "update_checker_dependency_outdated_info{"); n != 1 {
		t.Errorf("outdated series = %d, want 1 (only UpdateAvailable results)", n)
	}
}

// TestExporter_DigestDriftSeries makes the drift signal the UI already shows alertable.
func TestExporter_DigestDriftSeries(t *testing.T) {
	now := time.Now()
	repo := &testRepo{
		results: []scan.Result{
			{Source: "lab", Chart: "frontend", Dependency: "nginx", Type: "image", Kind: "Deployment", Protocol: "image", Path: "k8s/deploy.yaml", CurrentVersion: "latest", LatestVersion: "latest", DigestChanged: true, Note: "floating tag moved from sha256:1111 to sha256:2222", CheckedAt: now},
			{Source: "lab", Chart: "backend", Dependency: "redis", Type: "image", Kind: "StatefulSet", Protocol: "image", Path: "k8s/db.yaml", CurrentVersion: "7.2.4", LatestVersion: "7.2.4", CheckedAt: now},
		},
		scannedAt: now,
	}

	body := scrape(t, Handler(repo.snap, nil))
	if !strings.Contains(body, `update_checker_dependency_digest_drift{chart="frontend",dependency="nginx",kind="Deployment",path="k8s/deploy.yaml",protocol="image",source="lab",tag_kind="",type="image"} 1`) {
		t.Fatalf("drift series missing\n%s", body)
	}
	if strings.Contains(body, `update_checker_dependency_digest_drift{chart="backend"`) {
		t.Fatalf("a reference with an unchanged digest produced a drift series\n%s", body)
	}
}

// TestExporter_LabelValuesAreBounded: label values copied from scanned content must not reach Prometheus unbounded.
func TestExporter_LabelValuesAreBounded(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("a", 500)
	repo := &testRepo{
		results: []scan.Result{
			{Source: "lab", Chart: long, Dependency: long, Type: "image", Kind: long, Protocol: "image", Path: long, Scope: "minor", CurrentVersion: "1.0", LatestVersion: "2.0", UpdateAvailable: true, CheckedAt: now},
		},
		scannedAt: now,
	}

	body := scrape(t, Handler(repo.snap, nil))
	if strings.Contains(body, long) {
		t.Fatal("a 500-byte label value reached the scrape")
	}
	if n := strings.Count(body, strings.Repeat("a", maxLabelLen)+"…"); n != 4 {
		t.Errorf("bounded label values = %d, want 4 (chart, dependency, kind, path)\n%s", n, body)
	}
}

// TestExporter_LongMultibyteAndInvalidLabelsStillScrape: a cut multi-byte or non-UTF-8 label used to fail the whole scrape.
func TestExporter_LongMultibyteAndInvalidLabelsStillScrape(t *testing.T) {
	long := "a" + strings.Repeat("é", 150) // byte 200 falls inside an "é"
	repo := &testRepo{results: []scan.Result{
		{Source: "s", Chart: "c", Dependency: "d1", Type: "image", Path: long + "/deploy.yaml",
			CurrentVersion: "1.0.0", LatestVersion: "1.1.0", Scope: "minor", UpdateAvailable: true},
		{Source: "s", Chart: "c", Dependency: "d2", Type: "image", Path: "bad-\xff-name.yaml",
			CurrentVersion: "1.0.0", LatestVersion: "1.1.0", Scope: "minor", UpdateAvailable: true},
	}}
	body := scrape(t, Handler(repo.snap, nil))
	mustContain(t, body, `dependency="d1"`)
	mustContain(t, body, `dependency="d2"`)
	if !utf8.ValidString(body) {
		t.Error("/metrics body is not valid UTF-8")
	}
}

// TestExporter_FailedAndSkippedPerReference: what the UI shows per reference, the dashboard can chart too.
func TestExporter_FailedAndSkippedPerReference(t *testing.T) {
	repo := &testRepo{results: []scan.Result{
		{Source: "s", Type: "image", Dependency: "a", CheckError: "403", CheckErrorKind: errkind.Auth},
		{Source: "s", Type: "helm", Dependency: "c", CheckError: "boom"}, // no kind: an older scanner
		{Source: "s", Type: "image", Dependency: "d", Skipped: []scan.SkippedTag{{Tag: "v9"}, {Tag: "v8"}}},
		{Source: "s", Type: "helm", Dependency: "e"},
	}}
	body := scrape(t, Handler(repo.snap, nil))
	mustContain(t, body, `dependency="a",error_kind="auth"`)
	mustContain(t, body, `dependency="c",error_kind="other"`)
	if n := strings.Count(body, "update_checker_dependency_check_failed_info{"); n != 2 {
		t.Errorf("check_failed series = %d, want 2", n)
	}
	if !strings.Contains(body, `dependency="d",kind="",path="",protocol="",source="s",tag_kind="",type="image"} 2`) {
		t.Errorf("want dependency_skipped_tags = 2 for d:\n%s", body)
	}
}

// TestExporter_FollowerIsSilent: only the leader exports result metrics, so each alert fires once.
func TestExporter_FollowerIsSilent(t *testing.T) {
	repo := &testRepo{results: []scan.Result{{Source: "s", Dependency: "d", UpdateAvailable: true}}, scannedAt: time.Now()}
	body := scrape(t, Handler(repo.snap, func() bool { return false }))
	if strings.Contains(body, "update_checker_") {
		t.Errorf("a follower exported result metrics:\n%s", body)
	}
	mustContain(t, scrape(t, Handler(repo.snap, func() bool { return true })), "update_checker_last_scan_time")
}

// TestExporter_UntrustedReportValuesStillScrape: invalid UTF-8 and unknown kinds from a report must not fail the scrape.
func TestExporter_UntrustedReportValuesStillScrape(t *testing.T) {
	snap := func() report.Snapshot {
		return report.Snapshot{
			Repos: []report.RepositoryScan{{Status: report.RepoStatus{Repo: "bad-\xff", Error: &report.ErrorInfo{Kind: "\xffmade-up"}}}},
			Deps: []report.Dependency{{Status: report.DepStatus{Result: scan.Result{
				Source: "src-\xff", Type: "image", CheckError: "x", CheckErrorKind: "made-up"}}}},
		}
	}
	body := scrape(t, Handler(snap, nil))
	mustContain(t, body, `error_kind="other"`)
	mustContain(t, body, `update_checker_repository_scan_failed{kind="other"`)
}
