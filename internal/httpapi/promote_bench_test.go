package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances"
)

// BenchmarkPOST_Promote_3Replicas probes /promote with a stubbed
// rolling deployer reporting three instance results. The handler
// path is what the bar measures; the deployer is stubbed because
// the per-instance HTTP cost is exercised by the rolling package's
// own benches (and the live-stack integration is the ADR-0005.x
// gate, not a bar).
// Pre-registered bar (ADR-0005 §193): < 60 s / op.
func BenchmarkPOST_Promote_3Replicas(b *testing.B) {
	threeReplicas := deployer.DeployResult{
		Outcome: deployer.OutcomeOK,
		Instances: []deployer.InstanceResult{
			{URL: "http://markup-svc-1:8080", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
			{URL: "http://markup-svc-2:8080", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
			{URL: "http://markup-svc-3:8080", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
		},
	}
	deps, st, _, _, _ := newPromoteDeps(b, threeReplicas)
	deps.Discovery = stubDiscovery{targets: []instances.Instance{
		{URL: "http://markup-svc-1:8080", Env: "production"},
		{URL: "http://markup-svc-2:8080", Env: "production"},
		{URL: "http://markup-svc-3:8080", Env: "production"},
	}}
	h := putRule(b, st, []byte("alpha,rule,1.0,1\n"))
	handler := httpapi.Promote(deps)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(b, httpapi.PromoteRequest{
			Hash: string(h), Env: "production", Role: "champion", Operator: "bench-bot", Reason: "tick",
		}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("promote: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkConcurrentOperatorAPI_10Concurrent fires 10 concurrent
// /promote requests per round, one per logically-independent env
// (production-0 … production-9) — the topology ADR-0005 §194 names.
// fsstate's WAL serialises the underlying writes; the bar pins the
// analytic "< 10 ms contention at this concurrency" estimate by
// measuring p99 across the round.
// Pre-registered bar (ADR-0005 §194): < 500 ms p99.
func BenchmarkConcurrentOperatorAPI_10Concurrent(b *testing.B) {
	oneInstance := deployer.DeployResult{
		Outcome: deployer.OutcomeOK,
		Instances: []deployer.InstanceResult{
			{URL: "http://markup-svc-1:8080", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
		},
	}
	deps, st, _, _, _ := newPromoteDeps(b, oneInstance)
	const concurrency = 10
	envs := make([]string, concurrency)
	targets := make([]instances.Instance, 0, concurrency)
	for i := range envs {
		envs[i] = "production-" + string(rune('0'+i))
		targets = append(targets, instances.Instance{URL: "http://markup-svc-1:8080", Env: envs[i]})
	}
	deps.Discovery = stubDiscovery{targets: targets}
	h := putRule(b, st, []byte("alpha,rule,1.0,1\n"))
	handler := httpapi.Promote(deps)

	// Pre-encode one request body per env so the timed path has no
	// json.Encoder allocations.
	bodies := make([][]byte, concurrency)
	for i := range bodies {
		bodies[i] = promoteBody(b, httpapi.PromoteRequest{
			Hash: string(h), Env: envs[i], Role: "champion", Operator: "bench-bot", Reason: "tick",
		}).Bytes()
	}

	durations := make([]time.Duration, 0, b.N*concurrency)
	var mu sync.Mutex

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		wg.Add(concurrency)
		for g := 0; g < concurrency; g++ {
			body := bodies[g]
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "/promote", bytes.NewReader(body))
				rec := httptest.NewRecorder()
				start := time.Now()
				handler.ServeHTTP(rec, req)
				elapsed := time.Since(start)
				mu.Lock()
				durations = append(durations, elapsed)
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	b.StopTimer()

	p99 := percentile(durations, 0.99)
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns/op")
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
