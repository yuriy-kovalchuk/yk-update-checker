// Package metrics exposes Prometheus-scrapeable metrics for scan results and
// operational health.  It is only registered when the binary runs inside a
// Kubernetes cluster (detected via rest.InClusterConfig).
package metrics

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// ResultCollector implements prometheus.Collector by reading live state from
// a scan.Repository and emitting gauge metrics for outdated dependencies.
type ResultCollector struct {
	repo      scan.Repository
	subsystem string // metric prefix subsystem name
}

// NewResultCollector creates a collector backed by the given repository.
func NewResultCollector(repo scan.Repository) *ResultCollector {
	return &ResultCollector{repo: repo, subsystem: "update_checker"}
}

// Describe satisfies prometheus.Collector.  The metrics have variable labels
// so we send nil descriptors (see prometheus.VarMetric).
func (c *ResultCollector) Describe(_ chan<- *prometheus.Desc) {}

// Collect reads the latest scan results and emits one gauge metric per outdated
// dependency.  Only results with UpdateAvailable == true are exposed.
func (c *ResultCollector) Collect(ch chan<- prometheus.Metric) {
	results, _, err := c.repo.Load()
	if err != nil {
		slog.Error("metrics: failed to load results for collection", "error", err)
		return
	}

	desc := prometheus.NewDesc(
		prometheus.BuildFQName("", c.subsystem, "dependency_outdated_info"),
		"Outdated dependency detected (1 = outdated, 0 = up-to-date). "+
			"Presence of a time series means an update is available.",
		[]string{
			"source", "chart", "dependency",
			"type", "protocol", "scope",
			"current_version", "latest_version",
		}, nil,
	)

	for _, r := range results {
		if !r.UpdateAvailable {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			desc, prometheus.GaugeValue, 1,
			r.Source, r.Chart, r.Dependency,
			r.Type, r.Protocol, r.Scope,
			r.CurrentVersion, r.LatestVersion,
		)
	}
}

// Register registers the ResultCollector with the given prometheus registry.
func (c *ResultCollector) Register(reg prometheus.Registerer) {
	reg.MustRegister(c)
	slog.Info("metrics: result collector registered")
}
