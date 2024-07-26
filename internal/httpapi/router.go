package httpapi

import (
	"net/http"

	oteltrace "go.opentelemetry.io/otel/trace"

	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
	"github.com/helmedeiros/model-registry/internal/store"
)

// Deps is the bundle of observability hooks + read-only substrate
// accessors the Router weaves into the middleware chain. Every
// observability field is required; Artifacts is required when the
// read-only operator endpoints are mounted.
type Deps struct {
	AccessLog AccessSink
	Metrics   MetricsRecorder
	PanicSink PanicSink
	Tracer    oteltrace.Tracer
	Ready     Ready
	// Artifacts is the Reader projection of the artifact substrate;
	// the typed field enforces at compile time that the read-only
	// endpoints cannot mutate.
	Artifacts store.Reader
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
	if deps.Artifacts == nil {
		panic("httpapi.NewRouter: Deps.Artifacts is required")
	}
	mux := http.NewServeMux()
	mux.Handle("/healthz", chain(deps, "/healthz", Healthz()))
	mux.Handle("/readyz", chain(deps, "/readyz", Readyz(deps.Ready)))
	// /metrics deliberately stays outside the WithMetrics middleware so
	// a scrape does not amplify the counter. The remaining chain (trace
	// + access log + correlation id + recover) still applies so the
	// exposition path is observable in Jaeger + Kibana.
	mux.Handle("/metrics", chainNoMetrics(deps, "/metrics", metricsHandler))
	mux.Handle("/artifacts", chain(deps, "/artifacts", Artifacts(deps.Artifacts)))
	mux.Handle("/artifact/{hash}", chain(deps, "/artifact/{hash}", Artifact(deps.Artifacts)))
	mux.Handle("/artifact/{hash}/{member}", chain(deps, "/artifact/{hash}/{member}", ArtifactMember(deps.Artifacts)))
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
