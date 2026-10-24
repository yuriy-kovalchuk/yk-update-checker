package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/report"
)

// Handler serves /metrics for the reports in snapshot; result metrics only while leading (nil: always).
func Handler(snapshot func() report.Snapshot, leading func() bool) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewResultCollector(snapshot, leading))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
