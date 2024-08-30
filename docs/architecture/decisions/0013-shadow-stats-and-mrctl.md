# 13. Shadow stats endpoint + mrctl shadow

## Status

Accepted — `GET /shadow-stats?since=<duration>` returns the markup-svc shadow-Decider comparison metrics aggregated from Prometheus, and `mrctl shadow` renders them at the operator's terminal. The endpoint is disabled by default; setting `--shadow-stats-prom-url` (or `REGISTRY_SHADOW_STATS_PROM_URL`) activates it.

## Context

ADR-0032 in markup-svc landed five shadow comparison metrics (`markup_challenger_agreement_total`, `markup_challenger_one_sided_total`, `markup_challenger_eval_timeout_total`, `markup_challenger_eval_errors_total`, `markup_challenger_factor_delta`). ADR-0012 in this repo closed the data-plane push so the metrics actually tick once an operator promotes a challenger. The remaining gap: the operator running `mrctl` cannot see those metrics without leaving the registry — they must open Grafana (the markup-decide-overview dashboard added in pricing-observability), find the relevant panels, and reason about the numbers. The promote-to-champion decision lives in those numbers; making the operator click out of mrctl to read them adds friction to every shadow workflow.

This ADR mirrors ADR-0010 (business-stats) one layer up: a read-through Prometheus projection exposed at a registry endpoint, surfaced through a new mrctl subcommand. No new storage, no new metric, no new markup-svc work.

## Decision

### `internal/shadowstats`

A new port (`Reader`) and a Prometheus adapter (`PromReader`) parallel to `internal/businessstats`. The Stats struct carries:

- `AgreementRate` (true ÷ total) and `AgreementSamples` (increase over the window)
- `OneSidedChampionRPS` + `OneSidedChallengerRPS`
- `TimeoutRPS` + `ErrorRPS`
- `FactorDeltaP50` / `P95` / `P99` (histogram quantiles)

`PromReader` runs ten queries concurrently via `golang.org/x/sync/errgroup`. The window argument is formatted as `<seconds>s` (single-unit PromQL) — same fix as ADR-0010's `promDuration` helper. `NoopReader` returns `ErrDisabled` for the unwired-by-config case.

### `GET /shadow-stats`

Mirrors `/env/{env}/business-stats`'s posture: `since` clamps to `[1m, 24h]` (reuses ADR-0010's `parseSince`); 5m default; 6s upstream timeout. Errors: `503 shadow_stats_disabled` when the reader is `NoopReader`; `502 shadow_stats_upstream` on Prom failure; `400 invalid_since`. No auth (DMZ posture from ADR-0006 still holds).

The endpoint is NOT env-keyed because markup-svc emits the shadow metrics without an env label (per ADR-0032 markup-svc is single-feature single-env per process). Adding env filtering would require either a markup-svc change (label addition) or a registry-side translation through instance discovery; both are parked. The current shape reflects "markup-svc-wide shadow signal" rather than "per-env shadow signal".

### `mrctl shadow [--since 5m] [--json]`

Renders the view:

```
since:           5m0s
agreement:       0.9716 over 15000 samples
one-sided:       champion=0.0042 rps  challenger=0.0031 rps
eval:            timeout=0.0010 rps  error=0.0000 rps
factor delta:    p50=0.0050  p95=0.0300  p99=0.0470
```

The team's documented promote-to-champion gate is `agreement >= 0.99 AND samples >= 10_000 AND factor_delta_p99 <= 0.05`. The command does NOT enforce the gate — it surfaces the numbers; the operator reads them and decides. Pre-registering the gate threshold here keeps the operator's promotion decision auditable: the standing rule is the threshold, not the operator's gut.

## Consequences

### Positive

- The shadow workflow is end-to-end inside mrctl. An operator running `mrctl promote --role challenger` and `mrctl shadow` covers everything from "ship the challenger" to "read the comparison" to "decide whether to promote" without leaving the terminal.
- The registry-as-projection pattern from ADR-0010 generalises cleanly. The two endpoints are siblings; future per-source metric projections follow the same shape (port + adapter + handler + mrctl subcommand).
- The promote-to-champion gate threshold is now documented in the same place as the tool that reads against it, so a new operator inherits the calibration along with the tool.

### Negative

- Ten Prometheus queries per `/shadow-stats` call. At operator cadence (≤ 20 `mrctl shadow` calls/day across the team) this is ≤ 200 prom queries/day — within retention budget. At dashboard cadence (1 call/min from a synthetic monitor) it is 10 prom queries/min (600/hour); non-trivial but not catastrophic, and the endpoint is not rate-limited. The `errgroup` fan-out is capped at `SetLimit(10)` so a hot endpoint cannot spike per-call goroutine count above the query count. Per-call latency at the fan-out is not yet measured; a `BenchmarkShadowStatsPromFanOut` bar is parked in the scientific-harness follow-up. This mirrors the same cost shape as ADR-0010.
- No env keying. A multi-feature future would need a translation layer; today's single-feature shape works because each markup-svc process maps to one env.
- The promote-to-champion gate is NOT enforced by mrctl. The thresholds are documented in this ADR and in the team runbook; a `mrctl promote --role champion --hash <prev-challenger>` succeeds whether or not the shadow metrics support it. The operator carries the gate.

### Deliberately not here

- `--gate` flag on `mrctl shadow` that exits non-zero when the thresholds are not met. A future addition; not needed for the first iteration where operators are still calibrating the thresholds against production data.
- Env keying. A markup-svc-side env label addition would let the registry filter per-env; tracked as a markup-svc follow-up.
- Auto-promote-from-shadow. A registry-side observer (analogous to ADR-0007's canary supervisor) that reads `/shadow-stats` periodically and promotes the challenger to champion when the gate clears. Powerful and parked; the operator-in-the-loop is the explicit choice for the first iteration.

## Alternatives considered

**Extend `/env/{env}/business-stats` to include shadow metrics** — would halve the endpoint count. Rejected: the business-stats payload is about customer-visible markup outcomes (factor distribution, decide rates); the shadow metrics are about operator-facing comparison quality. Mixing them in one envelope conflates two different decision surfaces and forces every business-stats consumer to parse fields they do not need.

**Put `mrctl shadow` behind a `--gate` flag that enforces the thresholds** — would let the operator gate the promote in one command. Rejected for v1: the thresholds are not yet calibrated against production data; baking them into a `--gate` flag now would either be wrong or premature. Document them in this ADR; revisit when there is real shadow traffic.

**Have markup-svc add an `env` label to the shadow metrics** — would let the registry filter per-env. Rejected for this commit: the work lives in markup-svc and the registry shape is already useful without it. The label addition is a markup-svc follow-up.
