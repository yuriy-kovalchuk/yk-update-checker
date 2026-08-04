package metrics

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Gauge holds operational Prometheus metrics for the scan service.
// Methods are called from Service.RunScan to record lifecycle events.
type Gauge struct {
	lastScanTime *prometheus.GaugeVec
	duration     prometheus.Summary
	total        *prometheus.CounterVec
}

// NewGauge creates operational metrics registered with the given registry.
func NewGauge(reg prometheus.Registerer) *Gauge {
	g := &Gauge{
		lastScanTime: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Subsystem: "update_checker",
			Name:      "last_scan_time",
			Help:      "Unix timestamp of the last completed scan.",
		}, nil),
		duration: prometheus.NewSummary(prometheus.SummaryOpts{
			Subsystem:  "update_checker",
			Name:       "scan_duration_seconds",
			Help:       "Duration of a scan run in seconds.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		}),
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: "update_checker",
			Name:      "scan_total",
			Help:      "Total number of scans performed.",
		}, []string{"status"}),
	}

	reg.MustRegister(g.lastScanTime, g.duration, g.total)
	slog.Info("metrics: operational gauges registered")
	return g
}

// Record dispatches to recordSuccess or recordFailure based on status.
// It satisfies the scan.MetricsCallback signature.
func (g *Gauge) Record(status string, elapsed time.Duration) {
	switch status {
	case "success":
		g.recordSuccess(elapsed)
	default:
		g.recordFailure(elapsed)
	}
}

// recordSuccess is called after a scan completes successfully.
func (g *Gauge) recordSuccess(elapsed time.Duration) {
	g.total.WithLabelValues("success").Inc()
	g.lastScanTime.With(prometheus.Labels{}).SetToCurrentTime()
	g.duration.Observe(elapsed.Seconds())
}

// recordFailure is called after a scan fails entirely (no results produced).
func (g *Gauge) recordFailure(elapsed time.Duration) {
	g.total.WithLabelValues("failure").Inc()
	g.duration.Observe(elapsed.Seconds())
}
