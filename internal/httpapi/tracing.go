package httpapi

import (
	"context"

	"go.opentelemetry.io/otel"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation scope registered against the
// global TracerProvider for handler-side child spans. Operators see
// this string as the span's scope in Jaeger.
const tracerName = "github.com/helmedeiros/model-registry/internal/httpapi"

// startChildSpan opens a child span off the request's span (set by
// WithServerSpan) using the package-level tracer.
func startChildSpan(ctx context.Context, name string) (context.Context, oteltrace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name)
}

// logInfoWithTrace emits an Info event whose attrs map is augmented
// with the trace_id + span_id pulled from ctx. Handlers use this for
// every event NOT emitted by the access-log middleware so an operator
// hopping from a Grafana alert ("registry.audit.write_failed fired")
// can land in the matching Jaeger trace. attrs is mutated in place;
// call sites always allocate a fresh map.
func logInfoWithTrace(logger AccessSink, ctx context.Context, msg string, attrs map[string]any) {
	if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs["trace_id"] = sc.TraceID().String()
		attrs["span_id"] = sc.SpanID().String()
	}
	logger.Info(msg, attrs)
}
