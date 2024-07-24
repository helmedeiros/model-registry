package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// BenchmarkWithRecoverNoPanic pins the defer-only cost on the path
// every non-panicking request takes. ADR-0003 budgets ~50 ns; this
// bench reads the actual frame overhead so the budget stays
// falsifiable.
func BenchmarkWithRecoverNoPanic(b *testing.B) {
	sink := &stubSink{}
	h := httpapi.WithRecover(sink, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
