# 5. Champion lifecycle and markup-svc deployer

## Status

Proposed — the write surface that closes the read-only loop ADR-0004 opened, the deployer that pushes promoted artifacts to running markup-svc instances via the body-based reload contract from markup-svc/ADR-0030, the static-config instance discovery the deployer reads, the Writer projections on `envstate` and `audit` that v0.0.3 stubbed with `ErrNotImplemented`, and the SQLite backings (`fsstate` + `fsaudit`) that survive a restart. This ADR proposes; subsequent commits land the code that satisfies it and the status flips to Accepted when a `mrctl upload --file rules.csv && mrctl promote --hash <h> --env <e>` round-trip is exercised end-to-end by an integration test that boots a registry, boots a markup-svc, drives the CLI, asserts the promoted CSV reflects in `markup-svc /decide`, and asserts every transition lands in `envstate` + `audit`.

## Context

v0.0.3 shipped the read-only surface: operators can see every artifact, every env's state, the operator action log. What v0.0.3 cannot do: change anything. Every Writer method returns `ErrNotImplemented`. v0.0.4 closes that loop with three operator-facing actions:

1. **Upload an artifact.** The CSV (and optional snapshot + diagnose members) lands in the substrate; the assigned content-address hash is returned to the caller.
2. **Promote a hash to an env's champion.** The env-state record is updated, an audit entry is written, and the deployer pushes the artifact body to every markup-svc instance behind the env.
3. **Rollback an env.** The previous champion is restored from history; the deployer pushes it to every markup-svc instance.

Three structural pieces enable this:

- **`internal/deployer/`** — the markup-svc HTTP client. Knows the body-based `/admin/reload` contract markup-svc/ADR-0030 established. Implements rolling push: per-replica health check between pushes, configurable parallelism + timeout, returns a `DeployResult` enumerating per-instance outcomes so a partial-failure promotion is observable rather than silently truncated.
- **`internal/instances/`** — static-config instance discovery. Maps `env name → []markup-svc base URL` from a config file or flag. Kubernetes-native discovery (the operator's API + EndpointSlices) is a separate ADR; this v0.0.4 contract does not depend on the runtime.
- **`envstate.Writer` + `audit.Writer` real impls** on the in-memory and SQLite backings. The typed contract has not moved since ADR-0004 — only the stubbed methods get bodies.

The SQLite backings (`fsstate` + `fsaudit`) move from the "next" list to the active scope because the lifecycle endpoints are the first that have to survive a process restart. The artifact substrate already runs on fsstore; the cmd shell now opens three SQLite files instead of one. Pattern is unchanged from fsstore (ADR-0002): a private connection pool with WAL + synchronous=FULL + busy_timeout pragmas applied through the DSN, schema bootstrapped at open time, every multi-statement operation under a tx.

This ADR does NOT change the auth posture. v0.0.4 still assumes the registry runs in a trusted network. RBAC + OIDC + API keys + multi-tenant write authorization is a separate ADR that lands when an operator points at a public network.

## Decision

### HTTP write surface

#### `POST /upload`

Body: `multipart/form-data` with parts:

| Part | Required | Content-Type | Note |
|---|---|---|---|
| `source` | yes | `text/csv` (the artifact's declared type) | the canonical rule definition. |
| `snapshot` | no | `application/json` | bre-go indexed-snapshot form. |
| `diagnose` | no | `application/json` | bre-go diagnose output (pre-computed). |
| `metadata` | no | `application/json` | `{created_by, source_commit_sha?, description?, derived_by_version?}`. |

Response: `200 {hash, state, diagnose}` where `diagnose` is the cached bre-go diagnose result so operators do not need to re-run the parser locally. `state` is `staged` on first upload and the existing state on a re-upload (idempotent per ADR-0002: same source bytes → same hash → same record).

Errors:

- `400 {status:"error",reason:"source_required"}` when the source part is missing.
- `400 {status:"error",reason:"unsupported_content_type"}` when `source` carries a media type the bre-go parser does not recognise.
- `400 {status:"error",reason:"diagnose_failed"}` when the source parses but the bre-go diagnose returns errors (rule conflicts, missing columns). The body includes the diagnose detail under `attrs.diagnose`.
- `413 {status:"error",reason:"upload_too_large"}` when `source` exceeds `--max-upload-bytes` (default 16 MB).

The handler emits `registry.artifact.uploaded` on the access log with attrs `{hash, content_type, size_bytes, created_by, outcome}` and records `registry_uploads_total{outcome}`.

#### `POST /promote`

Body: `application/json` with `{hash, env, role, operator, reason?}`. `role` is `champion` or `challenger`. v0.0.4 implements `champion` only; `challenger` returns `501 {status:"error",reason:"challenger_not_implemented"}` — 501 (not 400) because the caller's request is well-formed, the server simply does not implement that mode yet. The HTTP adapter translates `envstate.ErrNotImplemented` to 501 uniformly.

Response: `200 {env, previous_hash?, new_hash, deploy: {instances: [...InstanceResult], outcome: "ok"|"partial"|"failed"}}`.

`InstanceResult` shape: `{url, status: "deployed"|"failed"|"skipped", duration_ms, error?}`.

`deploy.outcome` rules:

- `ok` — every instance reports `deployed`.
- `partial` — at least one `deployed` and at least one `failed`. The env-state IS updated (the audit trail is the truth; failed instances need operator attention) but the response carries a 207-Multi-Status hint via `X-Partial-Deploy: true`.
- `failed` — zero instances reported `deployed`. The env-state is NOT updated; the upload survives but the promotion does not. Response is `502 Bad Gateway` with the per-instance details.

Errors:

- `400 invalid_role | invalid_env | invalid_hash | hash_unknown | hash_deprecated`.
- `502 deploy_failed` with `deploy: {instances: [...]}` populated.

The handler emits `registry.champion.promoted` (or `…rejected` when the deploy fails entirely) on the access log with attrs `{env, hash, previous_hash, operator, reason, outcome, instances_deployed, instances_failed}`, records `registry_promotions_total{env, role, outcome}` + `registry_deploys_total{env, target_instance, outcome}` + `registry_deploy_duration_seconds{env, role}`, and appends one `audit.Entry` per promotion attempt regardless of deploy outcome.

#### `POST /rollback`

Body: `application/json` with `{env, operator, reason}`. Recovers the previous champion from `envstate.History`, deploys it. Same response shape + outcome rules as `/promote`. Errors: `400 invalid_env | no_history | hash_deprecated`; `502 deploy_failed`.

Emits `registry.champion.rolled_back` + `registry_rollbacks_total{env, reason}`.

### `internal/deployer/`

The typed contract:

```go
type Deployer interface {
    // Deploy returns DeployResult for any reachable instances (with
    // per-instance Outcome) and a non-nil error only for infrastructure
    // failures the caller cannot inspect via the result envelope
    // (context cancellation, no instances configured). Callers branch on
    // both: error for "could not run the deploy at all", DeployResult
    // for "ran; here is what happened per instance".
    Deploy(ctx context.Context, instances []Instance, body Body) (DeployResult, error)
}

type Instance struct {
    URL  string
    Env  string
}

type Body struct {
    ContentType string
    Bytes       []byte
}

type DeployResult struct {
    Instances []InstanceResult
    Outcome   Outcome   // OutcomeOK | OutcomePartial | OutcomeFailed
}

type InstanceResult struct {
    URL      string
    Status   Status      // StatusDeployed | StatusFailed | StatusSkipped
    Duration time.Duration
    Error    error
}
```

`internal/deployer/rolling/` implements rolling push:

1. Send the body to instance N's `POST /admin/reload` with `Content-Type` set to `body.ContentType`.
2. Poll instance N's `/readyz` until 200 or `--instance-timeout` (default 10s) elapses.
3. If healthy: advance to N+1. If timed out: mark `failed`, advance to N+1.

Parallelism is configurable via `--deploy-parallelism` (default 1 — strict rolling so a bad deploy hits at most one instance at a time; ADR-0007 may raise the default once observability shows the safety case).

W3C `traceparent` is propagated from the operator's context into every `/admin/reload` + `/readyz` call so `mrctl promote → registry → markup-svc` renders as one trace.

### `internal/instances/`

```go
type Discovery interface {
    Instances(env string) ([]deployer.Instance, error)
}
```

`internal/instances/static/` reads a YAML or JSON config at `--instances-config` (or via `REGISTRY_INSTANCES_FILE`):

```yaml
production:
  - http://markup-svc-1:8080
  - http://markup-svc-2:8080
staging:
  - http://markup-svc-staging:8080
```

Empty / unknown env returns `ErrNoInstances` so `/promote` against an env with no configured markup-svc fleet fails fast at the boundary rather than on the first deploy attempt.

### `envstate.Writer` + `audit.Writer` implementations

Each method validates input + appends to history + updates state + writes an audit entry, under a single tx where the backing supports one. The typed contract is unchanged; only the bodies grow.

memstate's `PromoteChampion` runs under a single WLock covering the entire snapshot-and-write sequence: snapshot the current `state[env].Champion` hash, write the new `Role`, bump `UpdatedAt`, append the `Transition{Kind: KindChampionPromoted, FromHash, ToHash, Operator, Reason, At}`. A separate RLock-then-WLock would leave a TOCTOU window where a concurrent promote could change the champion between snapshot and write.

fsstate's equivalent runs under a write Tx that wraps the `UPDATE env_state` + `INSERT env_history` so a process kill mid-promotion either commits both rows or neither.

memaudit's `Record` validates the ULID + appends. fsaudit issues a single INSERT against the `audit_entry` PK — atomic at the SQLite statement level, no explicit Tx needed; ULID generation is the caller's responsibility (deployer + HTTP handlers).

### SQLite backings

`internal/envstate/fsstate/` + `internal/audit/fsaudit/`. Same connection-pool + DSN pragma pattern as fsstore. Schema from ADR-0004 §Read-model packages applied at open. Each runs through its existing `envstatetest` / `audittest` conformance suite. Pre-registered fsaudit micro-bars (`BenchmarkFsauditList_100Entries < 5 ms/op`, `BenchmarkFsauditRecord < 5 ms/op`) sit below ADR-0004's HTTP-layer bars so the substrate has room under the handler-level budget.

The cmd shell now opens three SQLite files under `--store-root`:

```
<store-root>/artifacts/       (fsstore)
<store-root>/envstate.db      (fsstate)
<store-root>/audit.db         (fsaudit)
```

Shutdown closes them in reverse order so audit writes drain last.

### `cmd/mrctl` write subcommands

```
mrctl upload --file <path> [--snapshot <path>] [--diagnose <path>] [--description …]
mrctl promote --hash <h> --env <e> --operator <o> [--reason …]
mrctl rollback --env <e> --operator <o> --reason <r>
```

Operator identity is `--operator` with `os.Getenv("USER")` as a convenience default. This carries no authentication guarantee and is overridden when the auth ADR lands — it is a placeholder so the audit trail has a human-meaningful value on the trusted-network deployment, not a security boundary. Reason is mandatory on rollback (auditability) and optional on promote. All three propagate W3C `traceparent` like the read subcommands.

### Observability surface this ADR adds

- **Traces:** `registry.artifact.upload`, `registry.champion.promote`, `registry.champion.rollback`, `registry.deploy.push_to_instance` server spans; `markup-svc.admin.reload` propagated child span.
- **Logs:** `registry.artifact.uploaded`, `registry.champion.promoted`, `registry.champion.rolled_back`, `registry.deploy.succeeded`, `registry.deploy.failed`.
- **Metrics:** `registry_uploads_total{outcome}`, `registry_promotions_total{env, role, outcome}`, `registry_rollbacks_total{env, reason}`, `registry_deploys_total{env, target_instance, outcome}`, `registry_deploy_duration_seconds{env, role}`, `registry_state_drift_total{env}` (per ADR-0001 metric set).
- **Alerts:** `RegistryUploadFailureRate`, `RegistryDeployFailureRate`, `RegistryStateDriftDetected`, `RegistryPromotionFailureRate` ship in the matching pricing-observability iteration with runbooks.

### Performance bars (pre-registered, per ADR-0012 protocol)

| Bench | Target | Note |
|---|---|---|
| `BenchmarkPOST_Upload_SmallArtifact` | < 200 ms | (pre-registered; bench pending) wraps `BenchmarkStorePut_SmallArtifact` (9.18 ms measured) with multipart parse + diagnose; the multipart + diagnose deltas are analytic estimates pending a `BenchmarkBreGoDiagnose_*` parked in Not closed. |
| `BenchmarkPOST_Upload_LargeArtifact` | < 1 s | (pre-registered; bench pending) wraps `BenchmarkStorePut_LargeArtifact` (9.98 ms measured) at the 2 MB scale; ~4-5× headroom over the upper diagnose estimate (9.98 ms + 200 ms ≈ 210 ms vs 1000 ms bar). Tighter bars need a measured diagnose bench. |
| `BenchmarkPOST_Promote_3Replicas` | < 60 s | rolling push across 3 instances at the 10 s per-instance timeout. 2× safety margin over the 3 × 10 s analytical worst-case so CI is not flaky on a single slow `/readyz` poll. Tighten after the first measured runs show real wall-clock. |
| `BenchmarkConcurrentOperatorAPI_10Concurrent` | < 500 ms p99 | ten concurrent `/promote` calls against ten distinct envs. The envs are logically independent but fsstate's WAL serializes the underlying writes; the bar pre-registers the analytic estimate (< 10 ms contention at this concurrency) — confirm on the first run before claiming "no contention" in prose elsewhere. |

## Consequences

### Closed

- The write HTTP surface is locked: paths, request bodies, response shapes, error envelopes, outcome enum (`ok | partial | failed`). Changes go through a follow-up ADR.
- The deployer's contract (`Deployer` interface + `DeployResult` shape) is the seam future deployers (e.g. blue/green, canary) wire against without breaking the HTTP layer.
- The rolling-push strategy is locked: per-instance health check between pushes, `--deploy-parallelism=1` default, partial-deploy commits the env-state. Changing the default parallelism requires an ADR.
- The static-config instance discovery shape is locked. Kubernetes-native discovery is a separate ADR; this contract gives that ADR an interface to satisfy.
- SQLite backings land at `<store-root>/envstate.db` + `<store-root>/audit.db`. Schema changes require a follow-up ADR + migration script.
- **Rollback after partial deploy** is safe by construction. If a `/promote` commits a partial deploy (some instances on the new champion, some on the old) and the operator then calls `/rollback`, the rollback path reads the previous champion from `envstate.History` and deploys it to every instance — including the ones that never received the partial promote. The fleet converges on a single hash regardless of the intermediate inconsistency. The rollback response may itself carry `X-Partial-Deploy: true` if a different instance fails this time; the audit trail records both attempts.

### Not closed

- **Challenger lifecycle + shadow mode** (ADR-0006): `POST /promote {role=challenger}` + the markup-svc shadow contract + the `registry_challengers_active` + `registry_gate_evaluations_total` + `registry_shadow_divergence_observed_total` metric set.
- **Promotion gates** (ADR-0006): declarative criteria evaluated against Prometheus queries before a challenger graduates.
- **CI/CD integration** (ADR-0007): auto-stage uploads on rules-repo pushes; cookbook recipes.
- **K8s-native instance discovery** (ADR-0008): the EndpointSlice-based variant of `internal/instances/`.
- **Auth on `/upload` / `/promote` / `/rollback`** (separate ADR): v0.0.4 still assumes a trusted network. OIDC / API keys / RBAC is the next operator-facing release after the data plane round-trip is proven.
- **`BenchmarkBreGoDiagnose_SmallArtifact` + `_LargeArtifact`**: the multipart + diagnose deltas the upload bars depend on are analytic estimates today. The scientific harness needs these benches before the upload bars graduate from pre-registered to measured.
- **`BenchmarkBootTime_ThreeSQLiteFiles`**: the boot-time estimate is analytic; the bench pins it before Accepted.

### Performance impact

Upload latency is dominated by the bre-go diagnose pass over the parsed CSV. The substrate `Put` cost (9.18 ms small / 9.98 ms large per v0.0.1 measurements) is the floor; multipart parse + diagnose adds an analytic estimate of 10–30 ms small / 50–200 ms large. No bre-go diagnose bench exists in the scientific harness as of this ADR, so the upper end of the large estimate is the loose-est input to the bar — see `BenchmarkBreGoDiagnose_*` in Not closed. The `< 200 ms` small bar and `< 1 s` large bar leave order-of-magnitude headroom over the upper estimate.

Promote latency is dominated by the per-instance health-check wait. The bench bar covers the analytical worst case (all N health checks fire at the full `--instance-timeout`); happy-path wall-clock is expected `< 5 s` for a 3-replica fleet and will be visible in the first measured run. Partial-failure paths are recorded by `registry_deploys_total{outcome=fail}` rather than absorbed by the bar.

Boot-time cost: opening three SQLite files instead of one adds an analytic estimate of ~100 ms cold-boot (2× ADR-0003's measured 50 ms single-file fsstore cold-open). `BenchmarkBootTime_ThreeSQLiteFiles` will pin the real number before Accepted. The cmd shell stays inside ADR-0003's `< 500 ms` cold-boot budget on the estimate.

Operational risk: a partial deploy commits the env-state. The reasoning is that the audit trail is the truth — the operator needs the failure visible (`X-Partial-Deploy: true`, `registry_deploys_total{outcome=fail}` ticking) rather than buried inside a transaction that the substrate rolls back. The alternative (atomic all-or-nothing across N HTTP calls to independent services) is not possible without a coordinator the platform does not have today. The trade is documented + alarmable rather than hidden.
