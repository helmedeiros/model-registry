# Scientific measurement set — model-registry v0.0.5

v0.0.5 closes monetization gaps #1-6 on top of v0.0.4: pre-promote Diagnose gate (ADR-0006), post-promote canary auto-rollback (ADR-0007), per-env write rate-limit (ADR-0008), challenger lifecycle (ADR-0009), business-outcome stats endpoint (ADR-0010), rule provenance + diff (ADR-0011). Every chunk added middleware to the /promote and /rollback hot paths; this report pins the v0.0.5 measurement set on top of v0.0.4's. Per the ADR-0012 protocol, every bar below is pre-registered BEFORE measurement; bars never move; honest framing.

This commit registers the bar. Measured numbers land in a follow-up commit after the bench has been run.

## Pre-registered bars (status: pending)

| Benchmark | Bar | Layer | Reference | Status |
|-----------|-----|-------|-----------|--------|
| `BenchmarkSustainedLoad_50Concurrent` | p99 ≤ 50 ms · p999 ≤ 100 ms | HTTP handler under sustained load | this report | implemented at `internal/httpapi/promote_bench_test.go`; bar enforced via `b.Errorf` |

The bar is the v0.0.5 successor of v0.0.4's `BenchmarkConcurrentOperatorAPI_10Concurrent` (500 ms p99 at 10-concurrent). The v0.0.5 bar is 10× tighter in absolute terms (50 ms vs 500 ms) at 5× the concurrency. The bar is intentionally generous relative to any observed in-process number because the ADR-0006-0011 middleware stack adds per-call overhead whose production ceiling is not yet precisely measured; the headroom leaves room for conditions the in-process bench does not exercise (real OTLP exporter, fsstate WAL, network).

## Method

- `BenchmarkSustainedLoad_50Concurrent` runs 50 concurrent /promote goroutines per round against the stubbed deployer + memstate substrate so the bench measures the handler chain + observability emission, NOT SQLite contention (fsstate's WAL serialisation is exercised by the v0.0.4 substrate micro-bars).
- Run with `go test -run NONE -bench BenchmarkSustainedLoad_50Concurrent -benchmem -benchtime=100x ./internal/httpapi/`.
- The bench does not execute during `make test` because `make test` passes no `-bench` flag. No build tag is applied (the file is `internal/httpapi/promote_bench_test.go` and runs only when `-bench` is passed explicitly).
- Each round fans out 50 envs (production-0 … production-49) so envstate locking happens per-env, not globally. Per-round latencies are recorded on each goroutine; p50/p95/p99/p999 reported via `b.ReportMetric`. The p99 + p999 are also gated via `b.Errorf` so a regression past the bar fails the bench instead of silently passing.
- Per-round latency includes goroutine-stack and httptest scaffold allocation (~145 KB / round at 50-concurrent). This is intrinsic to concurrent load simulation but means the timed numbers include goroutine scheduling overhead in addition to handler chain cost. `b.ReportAllocs()` is called so allocs/op is surfaced.

## Measured numbers — HTTP handler under sustained load

Pending. Filled in a follow-up commit after running.

## What this proves and what it does not

This bench, once run, proves the post-ADR-0011 handler chain + observability emission stays within the pre-registered bar at 50-concurrent in-process load. It does NOT prove:

- The full live-stack round-trip under 2000 QPS sustained (network + real markup-svc + real rolling deploy). That regime needs a separate measurement track — the v0.0.5 in-process numbers are a lower bound on production latency, not an upper bound. The stubbed substrate and in-process tracer hide network, WAL serialisation, and OTLP export costs that production must pay.
- The behaviour under sustained rate-limit pressure. The bench uses 50 distinct env labels so each env's bucket has plenty of tokens; a different bench would need to drive against a single env to measure the rate-limited code path.
- The behaviour with the live OTel exporter. The bench wires the in-process tracetest tracer; a separate run with the real OTLP exporter would expose any I/O cost the in-process version hides.

These three gaps are tracked as parked follow-ups.

## Live-stack proof

The full live-stack verification continues to run via `scripts/verify-registry-observability.sh` in pricing-observability. That script's commit `575a919` extended it to cover ADRs 0006-0011. It exercises each new surface against a real markup-svc + Prometheus + Jaeger + Elasticsearch + AlertManager + alert-sink. It is not a benchmark but it is the end-to-end correctness gate the in-process bench does not replicate.
