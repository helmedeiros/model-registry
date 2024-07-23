// Package prom provides the Prometheus-backed HTTP metrics adapter for
// model-registry (ADR-0003).
package prom

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// httpBuckets covers the latency range model-registry endpoints span:
// healthz/readyz/metrics at sub-millisecond, future upload at hundreds
// of milliseconds, future rolling-push promote at multi-second. The
// default prometheus.DefBuckets starts at 5 ms and skips the sub-ms
// detail the substrate-only path needs to see in Grafana.
var httpBuckets = []float64{
	0.0001, 0.0005, 0.001, 0.005,
	0.025, 0.1, 0.5, 2.5, 10,
}

// HTTPMetrics holds the day-one metric set committed in ADR-0003 plus a
// private *prometheus.Registry so two HTTPMetrics in one process never
// collide on the global DefaultRegisterer.
type HTTPMetrics struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	handler  http.Handler
}

// New constructs a HTTPMetrics with the registry_http_* family
// registered against a private *prometheus.Registry. The matching
// /metrics handler is built once and cached so per-scrape Handler()
// access is allocation-free.
func New() *HTTPMetrics {
	reg := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_http_requests_total",
			Help: "Total HTTP requests served by model-registry, labelled by method / path / status (ADR-0003).",
		},
		[]string{"method", "path", "status"},
	)
	duration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "registry_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds, labelled by method / path (ADR-0003).",
			Buckets: httpBuckets,
		},
		[]string{"method", "path"},
	)
	reg.MustRegister(requests, duration)
	return &HTTPMetrics{
		reg:      reg,
		requests: requests,
		duration: duration,
		handler:  promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}
}

// RecordRequest increments the per-request counter and records the
// duration in the histogram. Path MUST be the matched-route template
// (e.g. "/healthz"), not the raw request URI; raw URIs blow up the
// label cardinality. The middleware that calls RecordRequest is the
// compile-time owner of that invariant.
func (m *HTTPMetrics) RecordRequest(method, path, status string, duration time.Duration) {
	m.requests.WithLabelValues(method, path, status).Inc()
	m.duration.WithLabelValues(method, path).Observe(duration.Seconds())
}

// Handler returns the cached promhttp handler bound to the private
// registry. Mounted by the service shell at /metrics.
func (m *HTTPMetrics) Handler() http.Handler { return m.handler }
