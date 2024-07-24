package httpapi

import (
	"net/http"

	oteltrace "go.opentelemetry.io/otel/trace"

	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
)

// Deps is the bundle of observability hooks the Router weaves into
// the middleware chain. Every field is required — the chain has no
// optional links.
type Deps struct {
	AccessLog AccessSink
	Metrics   MetricsRecorder
	PanicSink PanicSink
	Tracer    oteltrace.Tracer
	Ready     Ready
}

// NewRouter returns an http.Handler serving the substrate-only HTTP
// surface (/healthz, /readyz, /metrics) wrapped in the ADR-0003
// middleware chain, applied outermost to innermost:
//
//	WithRecover → WithCorrelationID → WithServerSpan
//	  → WithAccessLog → WithMetrics → handler
//
// metricsHandler is the Prometheus exposition handler the
// observability/metrics/prom adapter returns; the Router mounts it at
// /metrics without an enclosing WithMetrics so a scrape does not
// self-record.
func NewRouter(deps Deps, metricsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", chain(deps, "/healthz", Healthz()))
	mux.Handle("/readyz", chain(deps, "/readyz", Readyz(deps.Ready)))
	// /metrics deliberately stays outside the WithMetrics middleware so
	// a scrape does not amplify the counter. The remaining chain (trace
	// + access log + correlation id + recover) still applies so the
	// exposition path is observable in Jaeger + Kibana.
	mux.Handle("/metrics", chainNoMetrics(deps, "/metrics", metricsHandler))
	return mux
}

func chain(deps Deps, route string, handler http.Handler) http.Handler {
	handler = WithMetrics(deps.Metrics, route, handler)
	handler = WithAccessLog(deps.AccessLog, handler)
	handler = regotel.WithServerSpan(deps.Tracer, route)(handler)
	handler = WithCorrelationID(handler)
	handler = WithRecover(deps.PanicSink, handler)
	return handler
}

func chainNoMetrics(deps Deps, route string, handler http.Handler) http.Handler {
	handler = WithAccessLog(deps.AccessLog, handler)
	handler = regotel.WithServerSpan(deps.Tracer, route)(handler)
	handler = WithCorrelationID(handler)
	handler = WithRecover(deps.PanicSink, handler)
	return handler
}
