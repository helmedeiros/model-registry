# 10. Business-outcome statistics endpoint

## Status

Accepted — `GET /env/{env}/business-stats?since=<duration>` returns the live customer-visible markup metrics from markup-svc (`markup_decide_total`, `markup_factor_seconds`) aggregated by env and outcome. The endpoint is read-only, no auth, and disabled by default. It activates only when `--business-stats-prom-url` (or `REGISTRY_BUSINESS_STATS_PROM_URL`) is set. The aggregation runs in a new `internal/businessstats` port. `PromReader` is the production adapter; `NoopReader` and `ErrDisabled` cover the disabled-by-config case for the handler.

## Context

ADRs 0001-0009 closed the operational arc: upload → diagnose → promote → deploy → canary → throttle → challenger. Every signal those gates use — `registry_promotions_total`, `registry_canary_decisions_total`, `markup_decide_total` — is operational. None of them answer the question the platform is supposed to answer for the business: "is this champion making money".

markup-svc already emits the signals that do answer it:
- `markup_decide_total{env, outcome, rule}` — outcome distribution + per-rule fire counts.
- `markup_factor_seconds_bucket{env}` — the markup factor distribution (the actual customer-visible uplift the platform applies).

These metrics live in Prometheus. An operator currently has to leave the registry to read them (Grafana, or a hand-rolled `promtool query` call). The registry has every other piece of context — which hash is champion, when it was promoted, who rejected the last challenger — but it cannot answer "what is the current champion's factor p99 and decide rate". This ADR closes that gap with a thin aggregating endpoint, not by re-storing the metrics.

## Decision

### Port + adapter

```go
package businessstats

type Reader interface {
    Stats(ctx context.Context, env string, since time.Duration) (Stats, error)
}

type Stats struct {
    Env       string
    Since     time.Duration
    DecideRPS OutcomeRPS // ok/error/no_match/total rates over `since`
    FactorP50 float64
    FactorP95 float64
    FactorP99 float64
    TopRules  []RuleHit  // top N rules by fire-rate (default N=5)
}
```

`PromReader` runs eight queries against Prometheus's `/api/v1/query`, fanned out concurrently through `golang.org/x/sync/errgroup`:
- Four `sum(rate(markup_decide_total{outcome=..., env=...}[since]))` for ok / error / no_match / total.
- Three `histogram_quantile(q, sum by (le) (rate(markup_factor_seconds_bucket{env=...}[since])))` for p50 / p95 / p99.
- One `topk(N, sum by (rule) (rate(markup_decide_total{env=...}[since])))` for the top-rules block.

The window argument is formatted as `<seconds>s` (single-unit PromQL) rather than `time.Duration.String()`'s compound form (`5m0s`), which PromQL rejects. `total` is an independent query (no `outcome` filter); it can exceed `ok + error + no_match` if markup-svc emits an outcome label not covered by those three values.

`NoopReader` returns `ErrDisabled`. The handler translates `ErrDisabled` to `503 business_stats_disabled` so an operator running an older config without the prom URL gets an actionable error instead of a generic 500.

### Handler

`GET /env/{env}/business-stats[?since=5m]`:
- `since` accepts any Go duration in `[1m, 24h]`. Default 5m matches the canary window from ADR-0007 so the two surfaces agree on "recent". `<1m` and `>24h` reject with 400 `invalid_since` — sub-minute windows make `rate()` noisy on a Prometheus default 15s scrape, and >24h queries can exhaust the Prometheus instance.
- 6s upstream timeout, propagated through the errgroup so all eight concurrent prom queries share one deadline. Wall-time is one Prometheus round trip rather than the sum of eight (the previous sequential draft of this ADR called for nine sequential calls behind a 6s + 5s nested timeout — that arithmetic only worked at low Prometheus latency and is replaced by the concurrent fan-out). No wall-time benchmark exists yet; pre-registered bar for the harness: P99 ≤ 6s at the dashboard-cadence regime defined below.
- Errors: `503 business_stats_disabled` when the reader is `NoopReader`; `502 business_stats_upstream` when Prometheus rejects or 5xx's. `400` for `env_required` / `invalid_since`. No auth (DMZ assumption stands from ADR-0006).

### Wiring

`config.BusinessStatsPromURL` binds `--business-stats-prom-url` and `REGISTRY_BUSINESS_STATS_PROM_URL`. Empty = endpoint disabled, no route mounted. Set = `httpapi.Deps.BusinessStats` populated with a `PromReader`, route mounted at `/env/{env}/business-stats`.

### mrctl

`mrctl stats <env> [--since 5m] [--json]` mirrors `mrctl state`'s shape:
```
env:    production
since:  5m0s
decide: total=12.300 rps  ok=11.800  err=0.200  no_match=0.300
factor: p50=1.0500  p95=1.1800  p99=1.2400
top_rules:
  premium_uplift                60.000 rps
  loyalty_discount               4.200 rps
```

## Consequences

### Positive

- Operators see business state from the same CLI that drives every other registry verb. No tab-switching to Grafana for "what's my factor distribution right now".
- The endpoint is a read-through projection — no new storage, no migration, no double-source-of-truth between registry and markup-svc. If markup-svc adds a metric, the registry's view extends by adding one prom query, no schema change.
- The DMZ assumption stays valid: this is a read-only metric proxy, the same threat surface as `/metrics` already in front of every Pricing Decision Platform service.

### Negative

- Eight Prometheus queries per request, concurrent. At operator cadence (≤ 10 calls/day across all envs) this is < 100 Prometheus query-seconds per day — well within any retention budget. At dashboard cadence (1 call/min per env across 5 envs = 5 req/min) it becomes 5 × 8 = 40 prom queries/min, which is non-trivial; no measurement has been taken at that cadence and the endpoint is not rate-limited. The 1m floor on `since` prevents noisy rate windows but does not throttle call frequency.
- The aggregation shape is fixed at the registry. A future "p999" or "per-rule p99 factor" needs a code change here, not a Grafana edit.
- The endpoint inherits markup-svc's metric naming. If markup-svc renames `markup_decide_total` the registry breaks at runtime, not at compile time. Tracked as a known coupling — markup-svc changes those names rarely and never silently.

### Deliberately not here

- Pre-promote / post-promote business snapshot on the audit entry. Extending `audit.Entry` with a `BusinessSnapshot` JSON field is a wire-shape change that should ship under its own ADR alongside an explicit replay strategy.
- Auto-rollback on business-metric deterioration. The canary observer from ADR-0007 reads `markup_decide_total{outcome}` for that purpose. Driving rollback off factor-distribution drift is a richer signal but needs a threshold story that does not yet exist.
- Business metrics on the challenger. The shadow Decider in markup-svc Iteration 5 is what makes per-role business metrics possible; until then there is one decider, one outcome stream, and the endpoint reports the whole env.

## Alternatives considered

**Embed Prometheus client in the registry binary** — would let the endpoint short-circuit network hops. Rejected: it would couple the registry's deploy cycle to a metrics-storage upgrade and double the binary's surface area. The HTTP `/api/v1/query` call is well-defined and stable.

**Cache the most recent Stats per env** — would lighten the prom load if the endpoint ever became hot. Rejected for v1: no measurable hot path yet; an unobserved cache is a freshness hazard.

**Push business metrics from markup-svc to the registry** — would make the registry the source of truth. Rejected: doubles the storage layer, adds a queue, and gives the registry a problem (timeseries retention) it has no business solving. Prometheus is already that source.
