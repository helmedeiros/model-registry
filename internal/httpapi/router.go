package httpapi

import (
	"net/http"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/envstate"
	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
	"github.com/helmedeiros/model-registry/internal/store"
)

// Deps is the bundle of observability hooks + read-only substrate
// accessors the Router weaves into the middleware chain. Every
// field is required — ADR-0003's chain has no optional links and
// ADR-0004's read endpoints are all required when the chain is
// mounted.
type Deps struct {
	AccessLog AccessSink
	Metrics   MetricsRecorder
	PanicSink PanicSink
	Tracer    oteltrace.Tracer
	Ready     Ready
	// Reader-typed substrate fields enforce read-only at compile time
	// for the v0.0.3 read endpoints.
	Artifacts store.Reader
	EnvState  envstate.Reader
	Audit     audit.Reader
	// Upload carries the write dependencies for ADR-0005's POST /upload.
	// nil disables the route — the read-only chain in v0.0.3 did not
	// require a substrate writer.
	Upload *UploadDeps
	// Promote carries the write dependencies for ADR-0005's POST
	// /promote. nil disables the route.
	Promote *PromoteDeps
	// Rollback carries the write dependencies for ADR-0005's POST
	// /rollback. nil disables the route.
	Rollback *RollbackDeps
	// Reject carries the write dependencies for ADR-0009's POST
	// /reject. nil disables the route.
	Reject *RejectDeps
	// BusinessStats carries the read dependency for ADR-0010's
	// GET /env/{env}/business-stats. nil disables the route.
	BusinessStats *BusinessStatsDeps
	// ShadowStats carries the read dependency for ADR-0013's
	// GET /shadow-stats. nil disables the route.
	ShadowStats *ShadowStatsDeps
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
	validateDeps(deps)
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
	mux.Handle("/artifact/{from}/diff/{to}", chain(deps, "/artifact/{from}/diff/{to}", Diff(deps.Artifacts)))
	mux.Handle("/env/{env}/state", chain(deps, "/env/{env}/state", EnvState(deps.EnvState)))
	mux.Handle("/env/{env}/history", chain(deps, "/env/{env}/history", EnvHistory(deps.EnvState)))
	mux.Handle("/audit", chain(deps, "/audit", Audit(deps.Audit)))
	if deps.Upload != nil {
		mux.Handle("/upload", chain(deps, "/upload", Upload(*deps.Upload)))
	}
	if deps.Promote != nil {
		mux.Handle("/promote", chain(deps, "/promote", Promote(*deps.Promote)))
	}
	if deps.Rollback != nil {
		mux.Handle("/rollback", chain(deps, "/rollback", Rollback(*deps.Rollback)))
	}
	if deps.Reject != nil {
		mux.Handle("/reject", chain(deps, "/reject", Reject(*deps.Reject)))
	}
	if deps.BusinessStats != nil {
		mux.Handle("/env/{env}/business-stats", chain(deps, "/env/{env}/business-stats", BusinessStats(*deps.BusinessStats)))
	}
	if deps.ShadowStats != nil {
		mux.Handle("/shadow-stats", chain(deps, "/shadow-stats", ShadowStats(*deps.ShadowStats)))
	}
	return mux
}

func validateDeps(deps Deps) {
	required := []struct {
		ok   bool
		name string
	}{
		{deps.Artifacts != nil, "Artifacts"},
		{deps.EnvState != nil, "EnvState"},
		{deps.Audit != nil, "Audit"},
	}
	for _, r := range required {
		if !r.ok {
			panic("httpapi.NewRouter: Deps." + r.name + " is required")
		}
	}
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
