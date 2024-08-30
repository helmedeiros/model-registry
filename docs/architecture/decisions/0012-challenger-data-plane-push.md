# 12. Challenger data-plane push — rolling deployer reaches markup-svc's shadow admin surface

## Status

Accepted — `/promote role=challenger` no longer stops at the envstate write. After the audit record lands, the rolling deployer fans the source bytes out to each instance's `POST /admin/load-challenger` (markup-svc ADR-0031). `/reject` similarly fans `DELETE /admin/challenger` after clearing envstate. Push failure does NOT roll back the envstate change — the challenger is metadata-class, and the operator gets the registry-side state change AND a per-instance Deploy block reporting what happened across the fleet.

## Context

ADR-0009 closed the challenger lifecycle on the registry side: envstate writes, audit entries, response envelope. The actual challenger bytes never reached markup-svc. ADR-0031 in `markup-svc` added the admin surface (`POST /admin/load-challenger`, `DELETE /admin/challenger`); ADR-0032 in `markup-svc` made `/decide` consult the challenger and emit comparison metrics. The last gap: the registry knows the challenger hash, and markup-svc accepts and runs challenger bytes, but no code connects the two. An operator's `mrctl promote --role challenger` resulted in a hash sitting in registry envstate that markup-svc never saw.

This ADR closes that gap. The registry's existing rolling deployer (ADR-0005) already pushes champion bytes to `/admin/reload`; extending it to push challenger bytes to the new endpoint reuses the same fan-out, the same per-instance error model, the same span structure.

## Decision

### `deployer.Deployer` gains two methods

```go
type Deployer interface {
    Deploy(ctx, targets, body) (DeployResult, error)
    DeployChallenger(ctx, targets, body) (DeployResult, error)
    ClearChallenger(ctx, targets) (DeployResult, error)
}
```

`DeployChallenger` POSTs the source bytes to each target's `/admin/load-challenger` endpoint. `ClearChallenger` sends `DELETE /admin/challenger`. Both reuse the rolling implementation's per-instance loop, per-instance result envelope, OTel propagation, and instance-timeout posture. Span naming has two levels: the handler emits a parent span `registry.challenger.fan_out_push` covering the whole fan-out; each per-instance call emits a child `registry.deploy.challenger_push`. The clear path uses `registry.deploy.challenger_clear` (per-instance only — the reject handler does not wrap the fan-out in its own span yet).

The rolling implementation deliberately does NOT short-circuit on `StatusDiagnoseRejected` for challenger pushes. `Deploy` (champion) short-circuits because a Diagnose-rejected rule set is rejected by every instance the same way and continuing would only burn budget; the operator's only useful signal is "this rule set is unhealthy", which the first instance suffices to produce. The challenger lifecycle is metadata-class: the registry still wants per-instance acceptance so a mixed fleet (e.g. one instance running a stale binary that rejects on a kind the others accept) surfaces as `OutcomePartial`, not `OutcomeDiagnoseRejected`. To support that, `DeployChallenger` + `ClearChallenger` aggregate through a new `deployer.SummariseChallengerOutcome` that retains `OutcomeDiagnoseRejected` only when ALL instances reject and reports `OutcomePartial` when any deploy succeeded. `Deploy` is also followed by a `/readyz` poll; the challenger push does not poll — the challenger swap is metadata-only on the markup-svc side (no rolling restart, no traffic shift).

### `/promote role=challenger` failure semantics

The handler's order:

1. `GetBundle` + `GetMember(source)` — fail with `hash_unknown` / `substrate_error` / `hash_deprecated`. No envstate writes yet, safe to reject.
2. `Discovery.Instances(env)` — fail with `invalid_env` if no instances configured. No envstate writes yet.
3. `EnvState.PromoteChallenger` — commit. Fail with `envstate_error` (500).
4. `audit.Record("promote_challenger")` — best-effort, failure logged but does not fail the request (the envstate write committed).
5. `Deployer.DeployChallenger` — fan-out push. **Failure does NOT roll back envstate.** The handler records `registry_promotions_total{outcome="challenger_partial"|"challenger_failed"|"diagnose_rejected"}` and returns 200 with the Deploy block populated.

The non-rollback contract matters: a transient markup-svc outage during a challenger push must not erase the registry's intent. The operator can retry the push (by re-running `mrctl promote --role challenger --hash <h>` — the envstate UPSERT semantics from ADR-0009 mean re-promote is safe) or wait for the next reconciliation pass.

### `/reject` failure semantics

The handler's order:

1. Validate request.
2. `EnvState.Get(env)` for the previous challenger hash (used in the response and audit).
3. `EnvState.RejectChallenger` — commit. Fail with `no_challenger` (400) or `envstate_error` (500).
4. `audit.Record("reject_challenger")` — best-effort.
5. `Deployer.ClearChallenger` — fan-out DELETE. **Failure does NOT roll back envstate.** Outcome surfaces as `challenger_partial` / `challenger_failed` on `registry_rejects_total{outcome}`.

`Discovery + Deployer` are optional on `RejectDeps`. When unset, `/reject` clears envstate without a fan-out — preserves the pre-ADR-0012 behavior for deployments not yet wired for shadow.

### Wire types

`PromoteResponse.Deploy` already exists (from ADR-0005); it is now populated for `role=challenger` too. `RejectResponse` gains a `Deploy DeployView` field with the same shape.

### Metrics outcomes

`registry_promotions_total{role="challenger", outcome}`:
- `ok` — all instances accepted.
- `challenger_partial` — some accepted, some failed. Operator can investigate and retry without re-running envstate semantics.
- `challenger_failed` — no instance accepted. The registry-side intent stands; the data plane has no challenger.
- `diagnose_rejected` — every instance rejected on Diagnose (the loop visits all instances; a single Diagnose rejection on one instance + a successful deploy on another instance reports `challenger_partial` instead). The registry-side envstate write is now in a state where the registry thinks there is a challenger but the data plane cannot run it. Operator action: upload corrected bytes.

`registry_rejects_total{env, outcome}`:
- `ok` / `challenger_partial` / `challenger_failed` — same shape; no Diagnose case (DELETE has no body).
- `discovery_error` — `Discovery.Instances(env)` failed after envstate already cleared. The reject succeeded at the registry; the data-plane DELETE never ran. Operator should investigate the discovery backend and may re-issue the reject (idempotent on the registry side, will retry the fan-out).

## Consequences

### Positive

- The challenger lifecycle is end-to-end. `mrctl promote --role challenger` now causes markup-svc to actually run the rule set in shadow mode on every `/decide` (per markup-svc ADR-0032). The comparison metrics start ticking immediately.
- The push is observably distinct from the registry-side commit: operators can see in the Deploy block whether each instance accepted the bytes, and in the audit ledger the registry-side intent stands even if the fleet temporarily diverges.
- Non-rollback semantics survive transient markup-svc outages without operator intervention. The intent stays expressed; the operator retries the push or the next reconciliation pass closes the gap.

### Negative

- The `deployer.Deployer` interface grew by two methods. Three implementations updated in this commit (rolling, stubDeployer in two test files). Future test fakes must also implement the new methods. This is the cost of nominal Go interfaces.
- `/promote role=challenger` is no longer a metadata-only operation. It performs N sequential HTTP round-trips against markup-svc instances in addition to the envstate write. The wall-clock added by the fan-out is `sum(per-instance RTT)` for the happy path, bounded above by `N × instanceTimeout` (default `10 s`). For a 3-instance staging fleet with sub-50 ms instance RTTs the overhead is roughly 150 ms; for a 20-instance fleet with one transient timeout the overhead can hit 10 s. No `BenchmarkChallengerPushN` exists in the scientific harness yet; the latency bar is pre-registered as a follow-up before any number tighter than "RTT-sum bounded by N × instanceTimeout" is committed to ADR prose.
- Diagnose-rejected challenger pushes leave envstate in a "registry has a challenger that markup-svc cannot load" state. This is the same divergence shape `/promote role=champion` would create on Diagnose rejection — except for the champion path ADR-0006 short-circuits with HTTP 422 BEFORE the envstate write. The challenger path commits envstate FIRST (so the registry remembers the intent) and only then discovers the rejection. The asymmetry is intentional: champion rejection is fatal to the deploy attempt; challenger rejection is operator-actionable.

### Deliberately not here

- A pre-promote Diagnose gate for challenger. Would symmetrically eliminate the "registry says challenger / markup-svc says no" divergence. The same gate as ADR-0006 applies; not wired here to keep the commit small. The operator currently sees the Diagnose rejection in the response Deploy block and acts.
- Boot-time reconciliation. When markup-svc restarts, its challenger Holder is empty even though registry envstate may have a challenger. A reconciliation loop (registry periodically pushes envstate intent to all instances) is parked.
- Retry semantics inside the deployer. A failed push surfaces to the operator; the deployer does not retry. Adding bounded retry with exponential backoff is a follow-up if production load shows transient failures dominating.

## Alternatives considered

**Roll back envstate on push failure** — would keep "registry state == data plane state" as an invariant. Rejected: a transient markup-svc network blip would erase the operator's promote intent, and they would have to re-run the promote (re-fetching bytes, re-running audit) instead of just retrying the push. The metadata-class nature of the challenger justifies the lazier consistency model.

**Add a separate `/promote/challenger/push` endpoint** — would let the registry-side commit be observably distinct from the data-plane push. Rejected: it doubles the operator's mental model (two calls for one logical action) and the registry has all the information it needs to drive both inside one handler.

**Run the fan-out in the background after returning 200** — would make `/promote` faster for the operator. Rejected: the Deploy block in the response is the operator's only direct signal about data-plane state; making it asynchronous would force a separate polling loop (or accepting that the response is uninformative). Synchronous push within the handler's request budget is honest.
