//go:build bench

package shadowstats_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/shadowstats"
)

// BenchmarkShadowStatsPromFanOut drives PromReader.Stats against a
// stub Prometheus that responds in microseconds. The bench measures
// the 14-query concurrent fan-out plumbing (errgroup goroutines,
// context allocation, JSON decode, percentile post-processing).
// Bar pre-registered in scientific/v0.0.6/REPORT.md: p99 ≤ 50 ms / op.
func BenchmarkShadowStatsPromFanOut(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"metric": map[string]string{},
					"value":  []any{1.0, "1.0"},
				}},
			},
		})
	}))
	defer srv.Close()
	r := shadowstats.NewPromReader(srv.URL, shadowstats.WithPromClient(srv.Client()))

	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := r.Stats(context.Background(), 5*time.Minute)
		if err != nil {
			b.Fatalf("Stats: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()
	p50 := percentile(durations, 0.50)
	p95 := percentile(durations, 0.95)
	p99 := percentile(durations, 0.99)
	p999 := percentile(durations, 0.999)
	b.ReportMetric(float64(p50.Nanoseconds()), "p50-ns/op")
	b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns/op")
	b.ReportMetric(float64(p999.Nanoseconds()), "p999-ns/op")
	if p99 > 50*time.Millisecond {
		b.Errorf("p99 %v exceeds pre-registered 50 ms bar", p99)
	}
}

func percentile(xs []time.Duration, p float64) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(xs))
	copy(sorted, xs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}
