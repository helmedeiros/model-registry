package httpapi

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// traceIDFromCtx returns the W3C trace id of the span currently in
// ctx, or "" if no valid span context is set. Audit Record sites use
// this to populate audit.Entry.TraceID so the ledger entry hops back
// to the matching Jaeger trace.
func traceIDFromCtx(ctx context.Context) string {
	sc := oteltrace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
