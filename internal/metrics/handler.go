package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// Exporter bundles result collection and operational gauges behind a single
// HTTP handler suitable for mounting at /metrics.
type Exporter struct {
	handler http.Handler
	gauge   *Gauge
}

// NewExporter creates an exporter that reads live results from the repository
// and exposes both dependency and operational metrics on a Prometheus-compatible
// /metrics endpoint.
func NewExporter(repo scan.Repository) *Exporter {
	reg := prometheus.NewRegistry()
	collector := NewResultCollector(repo)
	collector.Register(reg)
	gauge := NewGauge(reg)

	return &Exporter{
		handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		gauge:   gauge,
	}
}

// Handler returns the HTTP handler for Prometheus metric scraping.
func (e *Exporter) Handler() http.Handler { return e.handler }

// Gauge returns the operational gauges so the scan service can record lifecycle events.
func (e *Exporter) Gauge() *Gauge { return e.gauge }
