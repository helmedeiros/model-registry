package otel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	reg "github.com/helmedeiros/model-registry/internal/observability/otel"
)

// BenchmarkServerSpan_NoopTracer pins the per-request cost of
// WithServerSpan when Bootstrap returned the SDK no-op tracer — the
// dominant production posture during dev + tests. ADR-0003 budgets
// ~300 ns for WithTraceContext on this path; the bench reads the
// actual cost of the propagator Extract + statusRecorder allocation
// + noop Start so the budget stays falsifiable.
func BenchmarkServerSpan_NoopTracer(b *testing.B) {
	tracer, shutdown, err := reg.Bootstrap(context.Background(), reg.Config{
		Exporter:            reg.ExporterNone,
		InstrumentationName: "bench",
	})
	if err != nil {
		b.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	wrapped := reg.WithServerSpan(tracer, "/healthz")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
	}
}
