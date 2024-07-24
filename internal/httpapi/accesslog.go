package httpapi

import (
	"net/http"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// AccessSink is the minimal interface WithAccessLog needs. *jsonlog.Logger
// satisfies it; tests inject a stub.
type AccessSink interface {
	Info(msg string, attrs map[string]any)
}

// WithAccessLog emits one structured event per request named
// registry.access with attrs {method, path, status, duration_ms,
// correlation_id, trace_id, span_id}. correlation_id is populated
// from the context value WithCorrelationID set; trace_id + span_id
// come from the active OTel span the WithServerSpan middleware
// opened. Place this middleware between WithTraceContext and
// WithMetrics in the chain so it sees both contexts.
func WithAccessLog(sink AccessSink, next http.Handler) http.Handler {
	if sink == nil {
		panic("httpapi.WithAccessLog: sink required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		attrs := map[string]any{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      sw.status,
			"duration_ms": float64(time.Since(start)) / float64(time.Millisecond),
		}
		if cid := CorrelationIDFromContext(r.Context()); cid != "" {
			attrs["correlation_id"] = cid
		}
		if sc := oteltrace.SpanContextFromContext(r.Context()); sc.IsValid() {
			attrs["trace_id"] = sc.TraceID().String()
			attrs["span_id"] = sc.SpanID().String()
		}
		sink.Info("registry.access", attrs)
	})
}
