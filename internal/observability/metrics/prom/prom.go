// Package prom provides the Prometheus-backed HTTP metrics adapter for
// model-registry (ADR-0003).
package prom

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	oteltrace "go.opentelemetry.io/otel/trace"
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

// HTTPMetrics holds the day-one metric set committed in ADR-0003 plus
// the v0.0.4 lifecycle counters that operators page on. Private
// *prometheus.Registry so two HTTPMetrics in one process never collide
// on the global DefaultRegisterer.
type HTTPMetrics struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	// Lifecycle counters per ROADMAP Iteration 4 §Observability.
	uploads     *prometheus.CounterVec
	promotions  *prometheus.CounterVec
	rollbacks   *prometheus.CounterVec
	deploys     *prometheus.CounterVec
	deployDur   *prometheus.HistogramVec
	stateDrift  *prometheus.CounterVec
	handler     http.Handler
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
	uploads := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_uploads_total",
			Help: "Total /upload calls, labelled by outcome (ok|invalid|too_large|substrate_error).",
		},
		[]string{"outcome"},
	)
	promotions := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_promotions_total",
			Help: "Total /promote calls, labelled by env / role / outcome (ok|partial|failed|hash_unknown|deprecated|invalid_env).",
		},
		[]string{"env", "role", "outcome"},
	)
	rollbacks := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_rollbacks_total",
			Help: "Total /rollback calls, labelled by env / outcome (ok|partial|failed|no_history|race_detected).",
		},
		[]string{"env", "outcome"},
	)
	deploys := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_deploys_total",
			Help: "Total instance deploys executed by the rolling deployer, labelled by outcome (deployed|failed|skipped).",
		},
		[]string{"outcome"},
	)
	deployDur := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "registry_deploy_duration_seconds",
			Help:    "Per-deploy wall-clock duration in seconds (one observation per /promote or /rollback call, across all instances).",
			Buckets: deployBuckets,
		},
		[]string{},
	)
	stateDrift := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "registry_state_drift_total",
			Help: "Total race-detected divergences between preview-state and committed-state during /rollback, labelled by env.",
		},
		[]string{"env"},
	)
	reg.MustRegister(requests, duration, uploads, promotions, rollbacks, deploys, deployDur, stateDrift)
	return &HTTPMetrics{
		reg:        reg,
		requests:   requests,
		duration:   duration,
		uploads:    uploads,
		promotions: promotions,
		rollbacks:  rollbacks,
		deploys:    deploys,
		deployDur:  deployDur,
		stateDrift: stateDrift,
		// EnableOpenMetrics carries exemplar lines through the /metrics
		// exposition so a Grafana panel can drill from a histogram bar
		// to the Jaeger trace whose id is on the exemplar.
		handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true}),
	}
}

// deployBuckets covers the per-instance push window: sub-second
// /admin/reload + a /readyz poll that may stall up to 10 s.
var deployBuckets = []float64{
	0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60,
}

// RecordUpload ticks the upload outcome counter.
func (m *HTTPMetrics) RecordUpload(outcome string) {
	m.uploads.WithLabelValues(outcome).Inc()
}

// RecordPromotion ticks the promotion outcome counter.
func (m *HTTPMetrics) RecordPromotion(env, role, outcome string) {
	m.promotions.WithLabelValues(env, role, outcome).Inc()
}

// RecordRollback ticks the rollback outcome counter.
func (m *HTTPMetrics) RecordRollback(env, outcome string) {
	m.rollbacks.WithLabelValues(env, outcome).Inc()
}

// RecordDeploy ticks the per-instance deploy counter once per
// instance result returned by the rolling deployer.
func (m *HTTPMetrics) RecordDeploy(outcome string) {
	m.deploys.WithLabelValues(outcome).Inc()
}

// ObserveDeployDuration records the wall-clock duration of one
// /promote or /rollback's deploy phase. When ctx carries a valid
// span context, the duration is recorded as an exemplar labelled
// with the trace_id so Grafana drills from a slow-bucket bar to
// the Jaeger waterfall that produced it.
func (m *HTTPMetrics) ObserveDeployDuration(ctx context.Context, d time.Duration) {
	obs := m.deployDur.WithLabelValues()
	if traceID := traceIDFromCtx(ctx); traceID != "" {
		if ex, ok := obs.(prometheus.ExemplarObserver); ok {
			ex.ObserveWithExemplar(d.Seconds(), prometheus.Labels{"trace_id": traceID})
			return
		}
	}
	obs.Observe(d.Seconds())
}

func traceIDFromCtx(ctx context.Context) string {
	sc := oteltrace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// RecordStateDrift ticks the race-detected divergence counter.
func (m *HTTPMetrics) RecordStateDrift(env string) {
	m.stateDrift.WithLabelValues(env).Inc()
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
