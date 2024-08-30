package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
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

// BenchmarkSustainedLoad_50Concurrent is the v0.0.5 sustained-load
// proxy. 50 concurrent /promote goroutines per round — 5× the
// v0.0.4 concurrent bar's concurrency — measures the additional
// middleware emission cost from ADRs 0006-0011 (Diagnose gate
// pre-check, canary observer dispatch, rate-limit Allow, audit
// envelope size with new outcomes). The substrate is memstate so
// the bench measures the handler chain + observability emission,
// NOT SQLite contention (fsstate's WAL serialisation is exercised
// by the v0.0.4 substrate micro-bars).
//
// Pre-registered bar (v0.0.5): p99 ≤ 50 ms · p999 ≤ 100 ms at
// 50-concurrent. The bar is intentionally generous relative to
// any observed in-process number because the ADR-0006-0011
// middleware stack adds per-call overhead whose production
// ceiling is not yet precisely measured; the headroom leaves
// room for conditions the in-process bench does not exercise
// (real OTLP exporter, fsstate WAL, network).
// Reports p50 / p95 / p99 / p999 + allocations.
func BenchmarkSustainedLoad_50Concurrent(b *testing.B) {
	oneInstance := deployer.DeployResult{
		Outcome: deployer.OutcomeOK,
		Instances: []deployer.InstanceResult{
			{URL: "http://markup-svc-1:8080", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
		},
	}
	deps, st, _, _, _ := newPromoteDeps(b, oneInstance)
	const concurrency = 50
	envs := make([]string, concurrency)
	targets := make([]instances.Instance, 0, concurrency)
	for i := range envs {
		envs[i] = "production-" + strconv.Itoa(i)
		targets = append(targets, instances.Instance{URL: "http://markup-svc-1:8080", Env: envs[i]})
	}
	deps.Discovery = stubDiscovery{targets: targets}
	h := putRule(b, st, []byte("alpha,rule,1.0,1\n"))
	handler := httpapi.Promote(deps)

	bodies := make([][]byte, concurrency)
	for i := range bodies {
		bodies[i] = promoteBody(b, httpapi.PromoteRequest{
			Hash: string(h), Env: envs[i], Role: "champion", Operator: "bench-bot", Reason: "tick",
		}).Bytes()
	}

	durations := make([]time.Duration, 0, b.N*concurrency)
	var mu sync.Mutex

	b.ReportAllocs()
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
	if p999 > 100*time.Millisecond {
		b.Errorf("p999 %v exceeds pre-registered 100 ms bar", p999)
	}
}
