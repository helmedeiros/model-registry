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
