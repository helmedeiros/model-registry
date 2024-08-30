# 9. Challenger lifecycle (registry side)

## Status

Accepted — `POST /promote` with `role=challenger` no longer returns 501. It stores the artifact hash against the env's challenger role in envstate, records `promote_challenger` to the audit ledger, and ticks the existing `registry_promotions_total{role="challenger"}` counter. `POST /reject` clears the env's challenger and records `reject_challenger`. The challenger envstate is metadata-only on the registry side; the actual shadow Decider that runs both champion and challenger on every `/decide` lives in markup-svc and is tracked in that repo's Iteration 5.

## Context

ADRs 0001-0008 carry the registry through the champion lifecycle: upload, promote, rollback, deploy, observe, gate, throttle. The `Challenger *Role` field on `envstate.State` and the `KindChallengerPromoted` / `KindChallengerRejected` / `KindChallengerEvaluated` transition kinds have existed since ADR-0004, but the Writer methods returned `ErrNotImplemented` and `/promote role=challenger` returned 501. The typed contract was in place; the lifecycle behind it was not.

A monetization platform needs the challenger primitive for safe experimentation: deploy a candidate rule set as a challenger, let markup-svc's shadow Decider run BOTH the champion and the challenger on every `/decide`, compare the answers, promote the challenger to champion only when the agreement rate (or business-outcome signal) clears a threshold. The shadow Decider is markup-svc's responsibility; the lifecycle plumbing — store the challenger hash, audit who promoted/rejected, expose it in the env state, paginate it in history — is the registry's.

## Decision

### envstate

`memstate.PromoteChallenger` and `RejectChallenger` are real implementations:
- `PromoteChallenger(ctx, env, hash, operator, reason)` UPSERTs the env's `Challenger` role, appends `KindChallengerPromoted` to history. Sticky: a second call replaces the challenger (no enforced uniqueness — promoting again is the way an operator iterates).
- `RejectChallenger(ctx, env, operator, reason)` requires a challenger to exist (returns new `ErrNoChallenger` sentinel otherwise), clears it, appends `KindChallengerRejected` to history.

`fsstate` ports the same semantics under one BeginTx per call. The Tx mirrors PromoteChampion / RollbackChampion's pattern: `monotonicAtMS` keeps the history `at` strictly increasing per env so the (env, at) PK never collides.

A new error sentinel `envstate.ErrNoChallenger` lets handlers distinguish "nothing to reject" from "envstate write failed".

### Handler

`POST /promote role=challenger` runs `runChallengerPromote`:
1. Resolves the artifact bundle (`hash_unknown` / `hash_deprecated` / `substrate_error` outcomes mirror the champion path).
2. Calls `EnvState.PromoteChallenger`.
3. Records `promote_challenger` audit action.
4. Returns 200 with `PromoteResponse{Env, NewHash}` (no `Deploy` block — there is no rolling push for a challenger today).
5. Ticks `registry_promotions_total{env, role="challenger", outcome="ok"}`.

`POST /reject` is a new handler with its own `RejectDeps` (small subset of RollbackDeps — no Discovery, no Deployer, since rejection is metadata-only). Validates `env`, `operator`, `reason`. Calls `EnvState.RejectChallenger`. Records `reject_challenger` audit action with the previous challenger hash as ArtifactHash. Ticks new counter `registry_rejects_total{env, outcome}`.

### Wire shape

```
POST /reject
  Request:  {env, operator, reason}        // reason is required, same posture as /rollback
  Response: {env, rejected_hash}           // rejected_hash is the previous challenger
  Errors:   400 invalid_body|invalid_env|invalid_operator|reason_required|no_challenger
            500 envstate_failed
```

### mrctl

`mrctl reject --env <e> [--operator <o>] --reason <r> [--json]` mirrors `mrctl rollback`'s shape. Exits 0 on success; surfaces the registry's reason on a 4xx through the existing `httpError` pattern (same machinery as `mrctl promote`'s 422 / `mrctl rollback`'s 429).

### What is deliberately NOT in this ADR

- markup-svc's shadow Decider. The registry stores the challenger hash and the data plane doesn't act on it until markup-svc Iteration 5 lands a Decider that runs both champion and challenger on every `/decide` and emits divergence metrics.
- Auto-promote-from-challenger. The pricing-observability stack will eventually carry `markup_shadow_divergence_total{env, agreement}` (a metric markup-svc will emit); a future registry-side observer pattern (analogous to ADR-0007's canary) will read it and propose a champion promotion. Today the operator's `mrctl promote --role champion --hash <challenger-hash>` is the promotion path.
- Challenger rate-limit. The ADR-0008 token bucket covers /promote role=champion + /rollback. /promote role=challenger and /reject are out of scope for this commit; an operator promoting and rejecting challengers at admin-rate is not yet a thrash vector worth its own bucket.
- ADR-0006's Diagnose gate. A challenger goes through the same Diagnose pre-promote gate as a champion because the gate runs at handler entry, before the role switch. (Confirmed by the existing TestPromoteDiagnoseRejected test still passing.)

## Consequences

### Closed

- The challenger role is operational on the registry side. An operator can `mrctl promote --hash <h> --env production --role challenger --reason "shadow week 3"` and the env's state shows it; `mrctl state production` exposes it via the existing `Challenger` field; `mrctl history production` carries the `challenger_promoted` transition; `mrctl audit` shows the `promote_challenger` action under the operator's identity.
- Rejecting a misbehaving challenger is a single `mrctl reject --env production --reason "agreement rate below threshold"`. The previous challenger hash surfaces in the response so an operator chasing a follow-up promotion can resurrect it.
- The audit ledger now distinguishes `promote` / `promote_challenger` / `reject_challenger` as separate actions. A "promotions per week" query is no longer skewed by challenger experiments.
- The fsstate backing persists challenger lifecycle transitions in the same env_history table; the existing `mrctl history` paginator picks them up without code change.

### Not closed

- Without markup-svc Iteration 5 the challenger is metadata-only. Promoting a challenger does NOT deploy anything to markup-svc — the bytes sit in the substrate, the envstate carries the hash, and that is the contract. The first observer to act on it will be markup-svc's shadow Decider; until then operators can use the challenger field for tracking and provenance but not for live comparison.
- The handler's challenger promote path does not invoke ADR-0007's canary supervisor. The canary's signal is the champion's effect on `markup_decide_total` — a challenger that never reaches `/decide` (because shadow mode isn't shipped yet) has no metric for the canary to watch.
- `ErrNotImplemented` is no longer used by envstate but the sentinel stays exported for future Writer slots (e.g. the auto-promote-from-challenger primitive when shadow mode lands).
