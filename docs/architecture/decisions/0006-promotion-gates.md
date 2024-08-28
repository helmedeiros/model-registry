# 6. Promotion gates

## Status

Accepted — `POST /promote` short-circuits with `422 promote_rejected` when the first markup-svc instance rejects the rule set's Diagnose verdict (markup-svc/ADR-0026). The deployer surfaces a new `StatusDiagnoseRejected` per-instance status and `OutcomeDiagnoseRejected` aggregate. The handler does not commit envstate, records `promote_rejected` to the audit ledger, and ticks the metric counter with `outcome=diagnose_rejected`. mrctl renders the rule-level issue list.

## Context

v0.0.4 shipped the operator-facing write surface. Today a syntactically valid rule set that fails markup-svc's Diagnose layer (duplicate rule names, dead rules, shadowed conditions) is treated identically to a sick markup-svc instance: the rolling deployer tries every instance in turn, each rejects with `400 + healthy:false`, the registry returns `502 promote outcome=failed`, the audit ledger records `outcome=failed`, the alert fires under `RegistryPromotionFailureRate`.

This collapses two operator concerns into one signal:
1. **Bad rule set** — the operator's CSV is broken; the data plane is fine.
2. **Bad data plane** — the CSV is fine; an instance is sick.

The two need different responses. (1) is "operator fixes their CSV and re-uploads". (2) is "page the markup-svc owner; data plane needs attention". Collapsing them into `outcome=failed` makes the alert noisy and the audit trail misleading. The rolling deployer also wastes `N * instance_timeout` re-rejecting the same bytes on every instance, when one rejection is enough to know the rule set is bad.

For a monetization platform the upside is bigger: a Diagnose gate is the first line of defense against a bad rule set reaching customers. v0.0.4 had no such gate.

## Decision

Promote a "Diagnose verdict" from a deploy outcome into a first-class promotion gate.

### Deployer surface

`internal/deployer.Status` gains `StatusDiagnoseRejected`. `Outcome` gains `OutcomeDiagnoseRejected`. `InstanceResult` gains `DiagnoseDetails *DiagnoseDetails` populated only when the instance returned the markup-svc reject body.

`rolling.Deploy` parses `400 + {healthy: false, errors, warnings}` responses from `/admin/reload` into a typed `diagnoseRejectedErr` and maps it to `StatusDiagnoseRejected`. The remaining instances are short-circuited with `StatusSkipped` and an error message naming the upstream rejection. `SummariseOutcome` treats `StatusDiagnoseRejected` as sticky — a single instance carrying it elevates the aggregate to `OutcomeDiagnoseRejected`.

### Handler surface

`POST /promote` checks for `OutcomeDiagnoseRejected` before the existing partial/failed branches:
- Returns `422 Unprocessable Entity` with a `PromoteRejectedResponse` envelope: `{env, hash, reason: "diagnose_rejected", diagnose: {healthy:false, errors, warnings}, deploy: <view>}`.
- Does NOT commit envstate.
- Records audit action `promote_rejected` (distinct from `promote`).
- Ticks `registry_promotions_total{env, role, outcome="diagnose_rejected"}`.

### CLI surface

`mrctl promote` recognizes the `httpError{status: 422}` returned by `postJSON` and re-parses the body as `PromoteRejectedResponse`. Renders the rule-level error/warning list to stderr in human-readable form and returns exit 1. `--json` mode emits the rejection envelope verbatim.

### Wire types

`internal/httpapi/wire_types.go` adds `PromoteRejectedResponse`, `DiagnoseDetailsView`, `DiagnoseIssueView`. Shapes mirror markup-svc's so a UI can render either source against one template.

## Consequences

### Closed

- Operators can distinguish "rule set is bad" from "data plane is sick" via the new metric outcome label, the new audit action, and the new HTTP status. The pricing-observability alert `RegistryPromotionFailureRate` already triggers on `outcome=~"failed|partial"` — `diagnose_rejected` does NOT trip that alert, which is correct: a rejected rule set is operator action, not a platform incident.
- A bad rule set produces ONE upstream call instead of N, saving `(N-1) × instance_timeout` wall-clock — `(N-1) × 10 s` at the ADR-0005 default, i.e. 20 s saved on a 3-instance fleet, 90 s on a 10-instance fleet. The skipped instances exit immediately with `StatusSkipped`; they never make the network call.
- The audit ledger records the gate decision separately from a real promotion, so a future "promotions per week by operator" view is not skewed by failed attempts.
- mrctl surfaces the issue list directly so the operator does not need to re-issue `/admin/diagnose` by hand.

### Not closed

- The gate runs on the FIRST instance, not pre-deploy. The bytes still travel one network hop to discover the rejection. A true pre-deploy gate (run Diagnose against the bytes BEFORE any deploy) requires either markup-svc to ship a `POST /admin/diagnose` endpoint or the registry to import bre-go's diagnose library directly. Both are bigger scope; today's "first-instance gate" closes the practical operator problem without the infra change.
- A new alert `RegistryDiagnoseRejectedRate` is NOT shipped this ADR — `diagnose_rejected` is operator action, not platform fault. If a future product decision wants to page on "too many operator typos", a separate alert lands then.
- Post-promote canary observation (auto-rollback on markup-svc decide error rate spike post-deploy) is parked.
- Promote rate-limit / per-env mutex is parked.
- Shadow / challenger lifecycle (ADR-0007 territory in this repo) is parked.
