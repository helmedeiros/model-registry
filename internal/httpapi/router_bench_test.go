package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkRouterChain_NoopTracer measures the full middleware chain
// (Recover → CorrelationID → ServerSpan → AccessLog → Metrics →
// handler) on the no-op-tracer path the production binary takes in
// dev / tests. ADR-0003 budgets < 100 µs total inbound overhead; this
// bench reads what the composed chain actually costs so the budget
// stays falsifiable.
func BenchmarkRouterChain_NoopTracer(b *testing.B) {
	deps, scrape := newRouterDepsForBench(b)
	r := newTestRouter(b, deps, scrape)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Correlation-ID", "11111111-2222-4333-8444-555555555555")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}
}
