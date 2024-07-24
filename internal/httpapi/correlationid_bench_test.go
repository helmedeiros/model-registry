package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// BenchmarkWithCorrelationIDHeaderPresent pins the per-request cost
// when the caller already supplied X-Correlation-ID — the dominant
// production path once traffic-gen / gateway start propagating IDs.
func BenchmarkWithCorrelationIDHeaderPresent(b *testing.B) {
	h := httpapi.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.CorrelationIDHeader, "11111111-2222-4333-8444-555555555555")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// BenchmarkWithCorrelationIDMinting pins the per-request cost when
// the caller did not supply X-Correlation-ID — the minting path that
// reads crypto/rand and encodes a UUID v4 into a fixed buffer.
func BenchmarkWithCorrelationIDMinting(b *testing.B) {
	h := httpapi.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
