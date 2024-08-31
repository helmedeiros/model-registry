//go:build bench

package rolling_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/deployer/rolling"
	"github.com/helmedeiros/model-registry/internal/instances"
)

// BenchmarkChallengerPushN3 drives DeployChallenger against 3 stub
// markup-svc instances. Bar pre-registered in scientific/v0.0.6/REPORT.md:
// p99 ≤ 100 ms / op. The bound covers the registry-side per-instance
// loop on a loopback stub; production fleets with real RTT scale
// linearly with instance count.
func BenchmarkChallengerPushN3(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rule_count":1,"model_version":"v0"}`))
	}))
	defer srv.Close()
	d := rolling.New(rolling.WithHTTPClient(srv.Client()))
	targets := []instances.Instance{
		{URL: srv.URL, Env: "production"},
		{URL: srv.URL, Env: "production"},
		{URL: srv.URL, Env: "production"},
	}
	body := deployer.Body{Bytes: []byte("alpha,a,1.0,1\n"), ContentType: "text/csv"}

	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, _ = d.DeployChallenger(context.Background(), targets, body)
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
	if p99 > 100*time.Millisecond {
		b.Errorf("p99 %v exceeds pre-registered 100 ms bar", p99)
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
