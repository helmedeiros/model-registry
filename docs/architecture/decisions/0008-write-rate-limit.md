# 8. Per-env rate limit on /promote + /rollback

## Status

Accepted — `POST /promote` and `POST /rollback` consult a per-env token bucket before any side-effect. A denial returns `429 Too Many Requests` with a `Retry-After` header and the standard `{status:"error", reason:"promote_rate_limited"}` (or `rollback_rate_limited`) envelope. The metric outcome `rate_limited` ticks on the relevant counter so a Grafana panel can show "thrash attempts" per env. mrctl renders the rejection.

## Context

v0.0.4 had no concurrency or rate protection on the write surface. The post-v0.0.4 `TestPromoteConcurrentSameEnvSerialises` proved that fsstate's WAL serialises concurrent writes correctly, so the registry is structurally safe from race-induced state corruption. What it is NOT safe from:

- An operator script in a retry loop that hammers /promote at every backoff tick.
- A misconfigured CI pipeline that pushes a new rule set on every commit, causing 60 deploys/min to bombard markup-svc's reload path.
- A rapid `promote → rollback → promote → rollback` thrash, intentional or accidental, that prevents the post-promote canary (ADR-0007) from observing a stable window.

The mitigations all live at the registry's HTTP edge: cap the per-env rate of lifecycle calls.

## Decision

### Port

`internal/ratelimit` introduces:
- `Limiter` interface: `Allow(key string) (ok bool, retryAfter time.Duration)`
- `TokenBucket`: per-key bucket, refills at `per` interval up to `burst` tokens; `Allow` consumes one if available and computes the time to the next token otherwise.
- `NoopLimiter` — always allows; used when the cmd shell disables the limiter.

### Handler integration

Both /promote and /rollback consult `deps.Limiter.Allow(req.Env)` immediately after request validation. On denial:
- Tick the corresponding outcome counter with label `rate_limited`.
- Set `Retry-After: <seconds>` header (ceiling-rounded so a 0.4 s wait still surfaces as 1 s).
- Return `429` with `{status:"error", reason:"promote_rate_limited"}` (or `rollback_rate_limited`).

Both endpoints share the same Limiter — promote and rollback compete for the same bucket per env so a thrash cycle hits the cap whichever direction it walks first.

### Configuration

| flag | env | default |
|---|---|---|
| `--write-rate-refill` | `REGISTRY_WRITE_RATE_REFILL` | `10s` (one token every 10 s) |
| `--write-rate-burst` | `REGISTRY_WRITE_RATE_BURST` | `2` (two immediate calls allowed) |

`--write-rate-refill=0s` or `--write-rate-burst=0` installs `NoopLimiter` — bit-for-bit v0.0.4 behaviour for the existing operator who has not adopted ADR-0008.

### CLI surface

`mrctl promote` and `mrctl rollback` detect the 429, read `Retry-After`, render `<op>_rate_limited: try again in <N>s` to stderr and exit 1. A retry script that calls mrctl in a loop respects the suggested wait without parsing the body.

## Consequences

### Closed

- A runaway script (operator-side bug, CI misconfig, intentional thrash) is capped at `burst + 1` calls per env per `refill` interval. At defaults that means 2 immediate + 1 every 10 s — sufficient for a real human operator + safety margin for chained `promote → rollback`, well below the rate at which markup-svc's reload path would saturate.
- ADR-0007's canary window is now stable: a rollback that fires during the canary's observation cannot be immediately followed by another /promote that resets the bucket — the canary keeps observing the rule set that was actually deployed.
- The 429 outcome label keeps the alert ergonomic: `RegistryPromotionFailureRate` deliberately matches `outcome=~"failed|partial"` (ADR-0019 in pricing-observability), so a thrash burst that lands `rate_limited` ticks does not page the markup-svc owner. A future `RegistryWriteThrashHigh` alert can target the `rate_limited` label specifically if operator coordination becomes a recurring concern.
- The Limiter is an opt-in single-line wiring on each Deps struct, so a test or in-process E2E can substitute `NoopLimiter` without touching the rate-limit logic.

### Not closed

- Single-process state. The token bucket lives in registry memory; two registry replicas behind a load balancer each enforce the cap independently, so a determined script bypasses by round-robining. The current deployment is single-replica (ADR-0003); when HA arrives, the bucket moves to a shared store (Redis/etcd) or sticks behind a session-affinity LB. The bucket map also grows with the number of distinct `env` keys; the current model (ADR-0003 static instance config) bounds envs to the operator-configured set so no eviction is required. If per-customer or otherwise dynamic envs land, the map needs an LRU cap or TTL eviction.
- The bucket is per-env, not per-operator. A second operator pushing to the same env shares the budget with the first. Per-operator allocations land alongside auth (parked).
- The defaults are a guess. An operator pushing pricing rules at the cadence of a daily standup would want a tighter cap (e.g. one per minute); a hot-fix scenario might want a looser one. Per-env overrides will land if operator feedback warrants.
- The cap is rate-only. A single /promote that succeeds + a runaway canary auto-rollback that fires N times in a row is shaped like a different problem — bounded by ADR-0007's "Not closed" thrash note rather than this ADR.
