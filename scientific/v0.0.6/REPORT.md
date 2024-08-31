# Scientific measurement set — model-registry v0.0.6

v0.0.6 closes the harness gap on the shadow-Decider surface that lives in this repo: the rolling deployer's challenger fan-out (ADR-0012) and the shadowstats Prometheus projection (ADR-0013). v0.0.5 covered the registry's HTTP handler under sustained load; v0.0.6 adds bars on the shadow paths that ADR-0012/0013 introduced and that earlier ADR prose claimed cost "tens of µs" or "non-trivial" without grounding.

The shadow `dispatchShadow` + sampling benches live in markup-svc (where the code lives) — they are a separate scientific iteration in that repo. This REPORT covers only the model-registry side.

Per the ADR-0012 protocol, every bar below is pre-registered BEFORE measurement; bars never move; honest framing.

This commit registers the bars. Measured numbers land in a follow-up commit after the benches have been run.

## Pre-registered bars (status: pending)

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

## Measured numbers

Pending. Filled in a follow-up commit after running.

## What these bars prove and what they do not

These bars prove the in-process cost of the shadow surface stays within an explicit ceiling. They do NOT prove:

- The live-stack cost under real Prometheus, real markup-svc, real network. The bench wires stub servers; production must pay round-trip latency the bench hides.
- The goroutine cost under contention. Bench runs are sub-millisecond at 100x — the Go scheduler is rarely under pressure. A separate bench at 50-concurrent would isolate the scheduler effect.
- The shadow comparison logic's accuracy. That is tested in `internal/httpapi/` handler tests and (end-to-end) in `pricing-observability/scripts/verify-registry-observability.sh`.

## Live-stack proof

The end-to-end shadow lifecycle is verified live via `verify-registry-observability.sh` in pricing-observability. That script's commit `007fe9b` extended it to cover the shadow surfaces. It is not a benchmark but it is the integration gate these in-process benches do not replicate.
