// Package metrics exports the stored reports to Prometheus.
package metrics

import (
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// maxLabelLen bounds label values whose text comes from scanned repository content (image names, file paths, chart names).
const maxLabelLen = 200

// identityLabels say *which* reference a series is about.
var identityLabels = []string{
	"source", "chart", "dependency",
	"type", "kind", "protocol", "path", "tag_kind",
}

// changeLabels describe the pending update. These are the volatile half: current_version/latest_version start a fresh series on every bump.
var changeLabels = []string{"scope", "current_version", "latest_version"}

// ResultCollector implements prometheus.Collector over the reports in a snapshot.
type ResultCollector struct {
	snapshot  func() report.Snapshot
	leading   func() bool
	subsystem string

	outdated *prometheus.Desc
	scanned  *prometheus.Desc
	drift    *prometheus.Desc
	failed   *prometheus.Desc
	skipped  *prometheus.Desc
	lastScan *prometheus.Desc
	repoFail *prometheus.Desc
}

// NewResultCollector creates a collector backed by the given repository.
func NewResultCollector(snapshot func() report.Snapshot, leading func() bool) *ResultCollector {
	c := &ResultCollector{snapshot: snapshot, leading: leading, subsystem: "update_checker"}
	c.outdated = prometheus.NewDesc(
		prometheus.BuildFQName("", c.subsystem, "dependency_outdated_info"),
		"Always 1; one series per reference with a newer version available.",
		append(append([]string{}, identityLabels...), changeLabels...), nil,
	)
	c.scanned = prometheus.NewDesc(
		prometheus.BuildFQName("", c.subsystem, "dependencies_scanned"),
		"References examined by the most recent scan that reached them, by source and type.",
		[]string{"source", "type"}, nil,
	)
	c.drift = prometheus.NewDesc(
		prometheus.BuildFQName("", c.subsystem, "dependency_digest_drift"),
		"Always 1; one series per image reference whose digest moved since the previous scan.",
		append([]string{}, identityLabels...), nil,
	)
	c.failed = prometheus.NewDesc(prometheus.BuildFQName("", c.subsystem, "dependency_check_failed_info"),
		"Always 1; one series per reference the latest scan could not check, with its errkind error_kind.",
		append(append([]string{}, identityLabels...), "error_kind"), nil)
	c.skipped = prometheus.NewDesc(prometheus.BuildFQName("", c.subsystem, "dependency_skipped_tags"),
		"Candidate tags rejected as strays for a reference; no series when none were.",
		append([]string{}, identityLabels...), nil)
	c.lastScan = prometheus.NewDesc(prometheus.BuildFQName("", c.subsystem, "last_scan_time"),
		"Unix time of the most recent successful repository scan.", nil, nil)
	c.repoFail = prometheus.NewDesc(prometheus.BuildFQName("", c.subsystem, "repository_scan_failed"),
		"1 when the repository's latest scan failed, else 0.", []string{"source", "kind"}, nil)
	return c
}

// Describe satisfies prometheus.Collector.
func (c *ResultCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.outdated
	ch <- c.scanned
	ch <- c.drift
	ch <- c.failed
	ch <- c.skipped
	ch <- c.lastScan
	ch <- c.repoFail
}

// Collect reads the latest scan results and reports them.
func (c *ResultCollector) Collect(ch chan<- prometheus.Metric) {
	if c.leading != nil && !c.leading() {
		return // followers stay silent so every alert fires once
	}
	snap := c.snapshot()
	c.collectRepos(ch, snap.Repos)
	results := make([]scan.Result, len(snap.Deps))
	for i, d := range snap.Deps {
		results[i] = d.Status.Result
	}

	var (
		seen       = make(map[string]struct{}, len(results))
		seenDrift  = make(map[string]struct{}, len(results))
		duplicates int
		scanned    = make(map[string]int, len(results))
		seenRef    = make(map[string]struct{}, len(results))
	)

	for _, r := range results {
		scanned[bound(r.Source)+"\xff"+bound(r.Type)]++
		id := identityValues(r)
		c.collectIssues(ch, r, id, seenRef)

		if r.DigestChanged {
			key := strings.Join(id, "\xff")
			if _, dup := seenDrift[key]; !dup {
				seenDrift[key] = struct{}{}
				ch <- prometheus.MustNewConstMetric(c.drift, prometheus.GaugeValue, 1, id...)
			}
		}
		if !r.UpdateAvailable {
			continue
		}

		// A duplicate label set makes the registry reject the series, and one rejected series fails the whole scrape.
		key := strings.Join(append(append([]string{}, id...), changeValues(r)...), "\xff")
		if _, dup := seen[key]; dup {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
		ch <- prometheus.MustNewConstMetric(c.outdated, prometheus.GaugeValue, 1,
			append(append([]string{}, id...), changeValues(r)...)...)
	}

	for compound, count := range scanned {
		source, typ, _ := strings.Cut(compound, "\xff")
		ch <- prometheus.MustNewConstMetric(c.scanned, prometheus.GaugeValue, float64(count), source, typ)
	}

	if duplicates > 0 {
		slog.Warn("metrics: results sharing every label were reported once", "count", duplicates)
	}
}

// collectIssues exports a reference's failed check and skipped tags, once per label set.
func (c *ResultCollector) collectIssues(ch chan<- prometheus.Metric, r scan.Result, id []string, seen map[string]struct{}) {
	key := strings.Join(id, "\xff")
	if _, dup := seen[key]; dup || (r.CheckError == "" && len(r.Skipped) == 0) {
		return
	}
	seen[key] = struct{}{}
	if r.CheckError != "" {
		ch <- prometheus.MustNewConstMetric(c.failed, prometheus.GaugeValue, 1, append(id, kindLabel(r.CheckErrorKind))...)
	}
	if len(r.Skipped) > 0 {
		ch <- prometheus.MustNewConstMetric(c.skipped, prometheus.GaugeValue, float64(len(r.Skipped)), id...)
	}
}

// collectRepos exports the latest successful scan time and each repository's failure state.
func (c *ResultCollector) collectRepos(ch chan<- prometheus.Metric, repos []report.RepositoryScan) {
	var last float64
	seen := map[string]struct{}{}
	for _, rs := range repos {
		if t := rs.Status.LastSuccessTime; t != nil && float64(t.Unix()) > last {
			last = float64(t.Unix())
		}
		failed, kind := 0.0, ""
		if e := rs.Status.Error; e != nil {
			failed, kind = 1, kindLabel(e.Kind)
		}
		source := bound(rs.Status.Repo)
		key := source + "\xff" + kind
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		ch <- prometheus.MustNewConstMetric(c.repoFail, prometheus.GaugeValue, failed, source, kind)
	}
	if last > 0 {
		ch <- prometheus.MustNewConstMetric(c.lastScan, prometheus.GaugeValue, last)
	}
}

// kindLabel keeps the kind label within errkind's vocabulary: a report is cluster input anyone can edit.
func kindLabel(k errkind.Kind) string {
	if slices.Contains(errkind.All, k) {
		return string(k)
	}
	return string(errkind.Other)
}

// identityValues and changeValues must keep the order of identityLabels and changeLabels: MustNewConstMetric pairs values with labels positionally.
func identityValues(r scan.Result) []string {
	return []string{
		bound(r.Source), bound(r.Chart), bound(r.Dependency),
		bound(r.Type), bound(r.Kind), bound(r.Protocol), bound(r.Path), bound(r.TagKind),
	}
}

func changeValues(r scan.Result) []string {
	return []string{bound(r.Scope), bound(r.CurrentVersion), bound(r.LatestVersion)}
}

// bound makes a label value safe to export.
func bound(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= maxLabelLen {
		return s
	}
	cut := maxLabelLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
