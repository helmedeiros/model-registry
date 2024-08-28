# 7. Post-promote canary observation + auto-rollback

## Status

Accepted — `/promote` spawns a goroutine after a successful deploy. The goroutine polls Prometheus for markup-svc decide error rate over a configurable window. On a sustained breach of the threshold the registry auto-rolls back the env to the previous champion and records the action in the audit ledger.

## Context

ADR-0006 closed the pre-promote gate (Diagnose-rejected rule set → 422 before the data plane sees the bytes). The post-promote gap remained: a syntactically valid + Diagnose-clean rule set can still misprice production traffic. v0.0.4 had no observation; the registry waited for a human operator to notice harm and call /rollback. For a monetization platform the response-time-to-bad-rule is what determines revenue loss; a human-in-the-loop window of minutes-to-hours can be material.

The pricing-observability stack already aggregates `markup_decide_total{outcome, env}` at scrape time. The registry already has the lifecycle endpoints (`RollbackChampion`, `Deploy`) that an automatic action would invoke. The missing primitive is "watch the metric, decide, act".

## Decision

### Port

`internal/canary` introduces:
- `Decision` enum {`kept`, `rolled_back`, `inconclusive`}
- `Observation` carrying the metrics that produced the decision (error rate, sample count, window, threshold) — surfaced on the audit entry so an operator sees WHY the registry decided what it did
- `Decider` interface — `Decide(ctx, env) (Decision, Observation, error)`
- `ErrUpstreamUnreachable` sentinel for "metric store didn't answer" — distinct from a legitimate `Inconclusive` (not enough samples)

`PromDecider` is the Prometheus-backed adapter. Polls
```
sum(increase(markup_decide_total{outcome="error",env=ENV}[WINDOW]))
sum(increase(markup_decide_total{env=ENV}[WINDOW]))
```
every `pollEvery` (default 30 s). Returns `RolledBack` as soon as `sample_count ≥ min_samples AND error_rate > threshold`. Returns `Kept` if the window elapses without a breach. Returns `Inconclusive` if the window elapses below `min_samples`.

Defaults: window 5 min, threshold 1%, poll 30 s, min samples 100. Configurable via `--canary-prom-url`, `--canary-window`, `--canary-threshold`, `--canary-poll-every`, `--canary-min-samples`. Empty `--canary-prom-url` disables the canary entirely (the supervisor pointer stays nil; the goroutine never spawns).

### Supervisor

`internal/httpapi.CanarySupervisor` is the glue. /promote calls `go deps.Canary.Observe(context.WithoutCancel(ctx), env, deployedHash, operator)` after the success response is written. The supervisor:

1. Calls `Decider.Decide(ctx, env)`.
2. Records a `canary_observed` audit entry carrying the decision + observation.
3. Ticks `registry_canary_decisions_total{env, decision}`.
4. On `RolledBack`: fetches the previous champion's bundle + source bytes, calls `Deployer.Deploy` against the env's instances, calls `EnvState.RollbackChampion`, records `auto_rollback` audit entry (operator: `registry.canary`, reason: the observation summary).
5. Logs `registry.canary.decision` (kept/inconclusive) or `registry.canary.auto_rollback` (rolled back) for the structured-log pipeline.

The goroutine runs under a `context.WithoutCancel` of the request context so the request's trace is the parent (operator's `mrctl promote` shows the canary as a descendant in Jaeger) but the request's deadline does not kill the canary mid-window.

### Audit + metric shapes

- `audit.Entry{Action: "canary_observed", Operator: "registry.canary"}` — one per decision.
- `audit.Entry{Action: "auto_rollback", Operator: "registry.canary"}` — one per rollback.
- `registry_canary_decisions_total{env, decision}` where decision ∈ {kept, rolled_back, inconclusive}.

A Grafana panel summing `rolled_back/(kept+rolled_back+inconclusive)` shows the canary's intervention rate. A spike means either the threshold is wrong (too tight) or operators are pushing too many bad rule sets.

### Configuration surface

| flag | env | default |
|---|---|---|
| `--canary-prom-url` | `REGISTRY_CANARY_PROM_URL` | empty (disabled) |
| `--canary-window` | `REGISTRY_CANARY_WINDOW` | `5m` |
| `--canary-threshold` | `REGISTRY_CANARY_THRESHOLD` | `0.01` |
| `--canary-poll-every` | `REGISTRY_CANARY_POLL_EVERY` | `30s` |
| `--canary-min-samples` | n/a | `100` |

## Consequences

### Closed

- A bad rule set that passes Diagnose but produces customer-visible errors is auto-reverted without operator action, bounded by `window` (the supervisor checks the deadline immediately after each sample; detection granularity is `poll_every`). With the defaults the supervisor issues at most `(window / poll_every) × 2 = 20` instant-query calls per `/promote`; an early threshold breach exits sooner.
- The decision + observation are auditable. `mrctl audit | grep auto_rollback` answers "did the registry intervene without us?". The observation reason carries the error rate, the threshold, and the sample count so an operator can challenge a false positive.
- The decision is also a metric label. Grafana shows the intervention rate over time; a future pricing-observability alert can page when the canary itself misbehaves (e.g. `rolled_back` rate spikes).
- The canary is opt-in. Empty `--canary-prom-url` ships the v0.0.4 behaviour bit-for-bit; existing operators see no change.

### Not closed

- A canary firing on its OWN rollback is not protected against. The rollback bytes get redeployed; another canary goroutine spawns; if the error rate stays high (it might — the rollback's rule set may not be the cause) a second auto-rollback walks the env one more step back. Today's mitigation is `--canary-window` × N goroutines bounded by the env's history depth. A future ADR can introduce a per-env supervision token (similar to a kubernetes deployment's pause field) to prevent thrash.
- No per-env tuning — `--canary-threshold` is global. Production-class envs may want a tighter threshold than experimental ones. A future ADR can land per-env config from the instances JSON.
- No business-outcome canary — error rate is the only signal. Revenue, conversion, or markup-factor distribution would be richer monetization signals.
- No A/B / shadow comparison — the canary is binary "kept or rolled back", not "ramped from 0% to 100%". Shadow mode is the natural escalation.
