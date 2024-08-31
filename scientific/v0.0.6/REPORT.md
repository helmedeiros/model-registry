# Scientific measurement set — model-registry v0.0.6

v0.0.6 closes the harness gap on the shadow-Decider surface that lives in this repo: the rolling deployer's challenger fan-out (ADR-0012) and the shadowstats Prometheus projection (ADR-0013). v0.0.5 covered the registry's HTTP handler under sustained load; v0.0.6 adds bars on the shadow paths that ADR-0012/0013 introduced and that earlier ADR prose claimed cost "tens of µs" or "non-trivial" without grounding.

The shadow `dispatchShadow` + sampling benches live in markup-svc (where the code lives) — they are a separate scientific iteration in that repo. This REPORT covers only the model-registry side.

Per the ADR-0012 protocol, every bar below is pre-registered BEFORE measurement; bars never move; honest framing.

## Pre-registered bars (status: measured)

| Benchmark | Bar | Layer | Reference | Status |
|-----------|-----|-------|-----------|--------|
| `BenchmarkChallengerPushN3` | p99 ≤ 100 ms / op | rolling deployer 3-instance fan-out against stub markup-svc | this report | implemented at `internal/deployer/rolling/bench_test.go`; gated via `b.Errorf` |
| `BenchmarkShadowStatsPromFanOut` | p99 ≤ 50 ms / op | shadowstats.PromReader 14-query concurrent fan-out | this report | implemented at `internal/shadowstats/bench_test.go`; gated via `b.Errorf` |

### Bar rationale

**BenchmarkChallengerPushN3** — 3-instance rolling fan-out matches the typical staging fleet size in ADR-0012's latency formula. Bar is the same order as the operator-quoted "~150 ms for 3 instances with sub-50 ms RTT"; we register 100 ms because the stub markup-svc loopback has near-zero RTT and the operator cost is dominated by the registry-side per-instance loop, not network. A real-fleet bench against actual markup-svc instances is parked.

**BenchmarkShadowStatsPromFanOut** — the operator-facing latency for `mrctl shadow`. Bar is the concurrent fan-out ceiling: 14 queries via errgroup with SetLimit(14) means wall-time is dominated by the slowest single query. Against a stub Prometheus that responds in microseconds, the bench measures the fan-out plumbing (goroutine scheduling, context allocation, JSON decode); the 50 ms bar leaves headroom for real-Prom RTT in production.

## Method

- Bench files carry `//go:build bench` so they do not execute during `make test`.
- Run with `go test -tags=bench -run NONE -bench <name> -benchmem -benchtime=100x <pkg>`.
- Each bench reports allocs/op via `b.ReportAllocs()`.
- Percentile-based bars use `b.ReportMetric` for p50/p95/p99/p999 plus a `b.Errorf` guard at the bar so a regression fails the bench instead of silently passing.

## Measured numbers (Apple M4, three-run medians)

Three consecutive runs of:

```
go test -tags=bench -count=3 -run NONE -bench <name> -benchmem -benchtime=100x <pkg>
```

| Statistic | `BenchmarkChallengerPushN3` | `BenchmarkShadowStatsPromFanOut` |
|-----------|----------------------------|----------------------------------|
| p50 | 240 µs | 429 µs |
| p95 | 284 µs | 730 µs |
| p99 | 444 µs | 813 µs |
| p999 | 444 µs | 813 µs |
| allocs / round | 455 | 2,570 |
| heap / round | 65 KB | 309 KB |
| Bar | ≤ 100 ms p99 | ≤ 50 ms p99 |
| Margin | ~225× under bar | ~62× under bar |

The `b.Errorf` gates did not fire in any of the three runs.

### Where the cost lives

**ChallengerPushN3** (444 µs p99 median): three sequential `http.Client.Do` calls against a loopback stub. The 455 allocs/round + 65 KB/round are dominated by `http.NewRequestWithContext` + `bytes.NewReader` + the OTel span lifecycle per instance (3 × per-call cost ~150 alloc / 22 KB). The bar covered the 3 × 10s `instanceTimeout` ceiling at the pessimistic end (30 s) and the operator-quoted "~150 ms with sub-50 ms RTT" at the realistic end. The actual stub-loopback number is ~225× faster than the bar — production will close most of that gap with network RTT.

**ShadowStatsPromFanOut** (813 µs p99 median): 14 concurrent `errgroup` queries against a stub Prometheus. The 2,570 allocs/round are 14 × per-query goroutine + http.Request + json.Decoder. The bar covered concurrent fan-out at real-Prom RTT; the stub-loopback number isolates the registry-side plumbing cost. Production will see real-Prom RTT dominate.

### Comparison to ADR-0012/0013 prose claims

| ADR claim | Source | Measured |
|-----------|--------|----------|
| "registry-side per-instance loop dominates over network for sub-50ms RTT" (ADR-0012) | analytic | 240 µs p50 / 444 µs p99 confirms loopback floor is well under any production RTT; claim stands |
| "non-trivial at dashboard cadence" for /shadow-stats (ADR-0013) | analytic | 813 µs p99 per call × 600 calls/hour = 488 ms of bench-equivalent CPU/hour; "non-trivial" was prudent but the absolute load is small |

## What these bars prove and what they do not

These bars prove the in-process cost of the shadow surface stays within an explicit ceiling. They do NOT prove:

- The live-stack cost under real Prometheus, real markup-svc, real network. The bench wires stub servers; production must pay round-trip latency the bench hides.
- The goroutine cost under contention. Bench runs are sub-millisecond at 100x — the Go scheduler is rarely under pressure. A separate bench at 50-concurrent would isolate the scheduler effect.
- The shadow comparison logic's accuracy. That is tested in `internal/httpapi/` handler tests and (end-to-end) in `pricing-observability/scripts/verify-registry-observability.sh`.

## Live-stack proof

The end-to-end shadow lifecycle is verified live via `verify-registry-observability.sh` in pricing-observability. That script's commit `007fe9b` extended it to cover the shadow surfaces. It is not a benchmark but it is the integration gate these in-process benches do not replicate.
