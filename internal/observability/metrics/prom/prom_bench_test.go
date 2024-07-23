package prom_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
)

// BenchmarkRecordRequest pins the per-request cost of the middleware's
// metrics hook so ADR-0003's < 100 µs total inbound overhead budget
// stays falsifiable. The bar allocates ~500 ns to RecordRequest; this
// bench reads the actual cost on Apple M4 / NVMe.
func BenchmarkRecordRequest(b *testing.B) {
	m := prom.New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.RecordRequest(http.MethodGet, "/healthz", "200", 150*time.Microsecond)
	}
}
