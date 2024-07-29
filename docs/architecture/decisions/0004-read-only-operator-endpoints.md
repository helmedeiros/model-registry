# 4. Read-only operator endpoints

## Status

Proposed — the read-only HTTP surface operators use to inspect the substrate without mutating it, the env-state and audit read-model packages those endpoints serve from, and the `cmd/mrctl` CLI skeleton that talks to them. This ADR proposes; subsequent commits land the code that satisfies it and the status flips to Accepted when every endpoint is exercised end-to-end by an integration test against the live server.

## Context

ADR-0003 shipped the service shell: `/healthz`, `/readyz`, `/metrics` through the ADR-locked middleware chain. The substrate (ADR-0002) is open behind `store.Store`. What the shell does not yet expose: the operator-facing surface for asking the substrate "what's in here?" and "what happened?" without touching live state.

ADR-0005 will add the write lifecycle (`POST /upload`, `POST /promote`, `POST /rollback`) and the markup-svc deployer. The read-only surface lands first so two stories land in one ADR-0004 cut:

1. **Operators can see what's there.** Listing artifacts, fetching a bundle by hash, reading the env-state model.
2. **Operators have a CLI before they have write endpoints.** `mrctl artifacts`, `mrctl artifact <hash>`, `mrctl state <env>`, `mrctl history <env>`, `mrctl audit`. Outbound HTTP propagates W3C `traceparent` so `mrctl → Registry → markup-svc` renders as one trace once the deployer ships.

Two read-model packages need new code:

- **`internal/envstate/`** — per-env champion + challenger hash table. Initialised empty; `GET /env/<env>/state` returns `{champion: null, challenger: null}` until v0.0.3 lifecycle endpoints populate it. The substrate's `Tag` exists, but per-env semantics (one champion per env, optional challenger) is a new concept the substrate does not own.
- **`internal/audit/`** — append-only operator action log. `GET /audit` returns the most recent N actions in reverse chronological order. The v0.0.3 write endpoints (upload, promote, rollback) emit entries.

Both packages follow the substrate's pattern: a small typed contract + in-memory backing + filesystem-SQLite backing + reusable conformance suite. Reusing the existing `internal/store/` `fsstore` SQLite handle is tempting — but the read-models and the artifact substrate evolve on independent schemas, so they get their own SQLite files under the same `--store-root`. ADR-0006+ may consolidate; this ADR does not.

## Decision

### HTTP surface

All endpoints mount under the existing ADR-0003 middleware chain. Routes use Go 1.22+ `net/http` `ServeMux` pattern wildcards.

#### `GET /artifacts`

Paginated list of artifact summaries. Query parameters:

| Param | Default | Note |
|---|---|---|
| `limit` | substrate default (100) | clamped to `store.MaxListLimit` (1000). |
| `cursor` | empty | opaque value from a previous response's `next_cursor`. |
| `state` | empty (all states) | one of `staged|active|deprecated`. |

Response: `200 {items: [...ArtifactSummary], next_cursor: "..."}`. `next_cursor` empty when the list is exhausted.

`ArtifactSummary` JSON shape:

```json
{
  "hash": "0d8a…",
  "content_type": "text/csv",
  "state": "active",
  "metadata": {
    "created_at": "2024-07-25T15:42:00Z",
    "created_by": "ci-bot",
    "source_commit_sha": "abc…",
    "description": "…",
    "derived_by_version": "bre-go@v0.16.0"
  }
}
```

Errors: `400 {status:"error",reason:"invalid_state"|"invalid_limit"|"invalid_cursor"}` on malformed query.

#### `GET /artifact/{hash}`

Single bundle metadata. Response: `200` with a `Bundle` JSON envelope (the same shape `store.Bundle` exposes minus internal fields):

```json
{
  "hash": "0d8a…",
  "content_type": "text/csv",
  "state": "active",
  "metadata": {...},
  "has_snapshot": true,
  "has_diagnose": false
}
```

Errors: `404 {status:"error",reason:"not_found"}` when the hash is unknown.

#### `GET /artifact/{hash}/source` (also `/snapshot`, `/diagnose`)

Member byte stream. Response body is the raw bytes from `store.Reader.GetMember`. Response headers:

- `Content-Type` — the artifact's declared `content_type` for `MemberSource`, `application/octet-stream` for the derived members (snapshot, diagnose).
- `Content-Length` — the byte count.
- `X-Artifact-Hash` — echoes the path parameter so a misaligned proxy is visible to the caller.

Errors: `404 {status:"error",reason:"not_found"}` when the hash is unknown; `404 {status:"error",reason:"member_absent"}` when the hash exists but the requested member was never uploaded.

#### `GET /env/{env}/state`

Current champion + challenger hashes for `env`. Until v0.0.3 lifecycle endpoints populate the env-state table, every env returns the empty state.

```json
{
  "env": "production",
  "champion": null,
  "challenger": null,
  "updated_at": null
}
```

When populated:

```json
{
  "env": "production",
  "champion": {"hash": "0d8a…", "promoted_at": "…", "promoted_by": "…"},
  "challenger": {"hash": "9f2c…", "promoted_at": "…", "promoted_by": "…"},
  "updated_at": "…"
}
```

`env` is a free-form string — the substrate does not validate the env name. Cardinality on the metric path is bounded by the operator's deliberate env-naming convention; the endpoint does not enforce a whitelist.

Errors: `404 {status:"error",reason:"unknown_env"}` is **not** returned — every env returns the empty state. This makes the endpoint safe to scrape from a dashboard before any operator action has touched the env.

#### `GET /env/{env}/history`

Reverse-chronological list of env state transitions. Same pagination shape as `/artifacts` (`limit`, `cursor`).

```json
{
  "items": [
    {
      "env": "production",
      "kind": "champion_promoted",
      "from_hash": "abc…",
      "to_hash": "0d8a…",
      "operator": "alice",
      "reason": "weekly canary graduation",
      "at": "…"
    }
  ],
  "next_cursor": "…"
}
```

`kind` is one of `champion_promoted | champion_rolled_back | challenger_promoted | challenger_rejected | challenger_evaluated`.

#### `GET /audit`

Reverse-chronological list of operator actions across all envs. Same pagination shape:

```json
{
  "items": [
    {
      "id": "01HXY…",
      "operator": "alice",
      "action": "promote",
      "target": "env/production/champion",
      "artifact_hash": "0d8a…",
      "reason": "…",
      "at": "…"
    }
  ],
  "next_cursor": "…"
}
```

### Read-model packages

#### `internal/envstate/`

```
type Reader interface {
    Get(ctx context.Context, env string) (EnvState, error)
    History(ctx context.Context, env string, opts ListOptions) (HistoryPage, error)
}

type Writer interface {
    PromoteChampion(ctx context.Context, env string, hash store.Hash, op, reason string) error
    RollbackChampion(ctx context.Context, env string) error
    PromoteChallenger(ctx context.Context, env string, hash store.Hash, op, reason string) error
    RejectChallenger(ctx context.Context, env string, reason string) error
}

type Store interface { Reader; Writer }
```

Writer methods exist on the typed contract but **no implementation lands in v0.0.3** — Iteration 4 (ADR-0005) supplies them. v0.0.3 implements only `Reader`; calling Writer methods returns `ErrNotImplemented`. This keeps the typed contract stable across the read-only release and the lifecycle release.

In-memory backing (`memstate`) for tests + a `storetest`-style conformance suite that any backing must satisfy. Filesystem-SQLite backing (`fsstate`) lives at `<store-root>/envstate.db` with two tables:

```sql
CREATE TABLE env_state (
    env             TEXT PRIMARY KEY,
    champion_hash   TEXT,
    challenger_hash TEXT,
    updated_at      INTEGER  -- nullable; null when never touched
);

CREATE TABLE env_history (
    env       TEXT NOT NULL,
    kind      TEXT NOT NULL,
    from_hash TEXT,
    to_hash   TEXT,
    operator  TEXT,
    reason    TEXT,
    at        INTEGER NOT NULL,
    PRIMARY KEY (env, at)
);

CREATE INDEX idx_env_history_recent ON env_history(env, at DESC);
```

#### `internal/audit/`

```
type Reader interface {
    List(ctx context.Context, opts ListOptions) (Page, error)
}

type Writer interface {
    Record(ctx context.Context, entry Entry) error
}

type Store interface { Reader; Writer }
```

Same dual-backing + conformance pattern. Filesystem backing lives at `<store-root>/audit.db`:

```sql
CREATE TABLE audit (
    id            TEXT PRIMARY KEY,  -- ULID for sortability + uniqueness
    operator      TEXT NOT NULL,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL,
    artifact_hash TEXT,
    reason        TEXT,
    at            INTEGER NOT NULL
);

CREATE INDEX idx_audit_recent ON audit(at DESC);
```

### Boot wiring

`cmd/model-registry/main.go` opens three Stores instead of one:

```go
artifactStore, _ := fsstore.New(filepath.Join(cfg.StoreRoot, "artifacts"))
envStore, _      := fsstate.New(filepath.Join(cfg.StoreRoot, "envstate.db"))
auditStore, _    := fsaudit.New(filepath.Join(cfg.StoreRoot, "audit.db"))
```

The `Deps` bundle the Router consumes grows two read-only accessors:

```go
type Deps struct {
    AccessLog  AccessSink
    Metrics    MetricsRecorder
    PanicSink  PanicSink
    Tracer     oteltrace.Tracer
    Ready      Ready
    Artifacts  store.Reader      // new
    EnvState   envstate.Reader   // new
    Audit      audit.Reader      // new
}
```

The Reader-typed fields enforce at compile time that the read-only endpoints cannot mutate. v0.0.3 cmd wiring satisfies them with the artifact-store `Reader` projection + the envstate/audit `Reader`-only implementations.

### CLI surface (`cmd/mrctl`)

Subcommands talk to a `--registry` flag (default `http://localhost:8090`). Outbound HTTP propagates W3C `traceparent` via the OTel propagator the binary initialises at boot (sharing the `internal/observability/otel` bootstrap; exporter defaults to `none` so a vanilla CLI run does not need an OTLP collector).

| Command | HTTP call | Output |
|---|---|---|
| `mrctl artifacts [--limit N] [--state S]` | `GET /artifacts` paginated; CLI follows cursors to print full N | TSV or `--json` |
| `mrctl artifact <hash>` | `GET /artifact/{hash}` | JSON pretty by default |
| `mrctl artifact <hash> source` | `GET /artifact/{hash}/source` | raw bytes to stdout |
| `mrctl state <env>` | `GET /env/{env}/state` | JSON |
| `mrctl history <env> [--limit N]` | `GET /env/{env}/history` paginated | TSV or `--json` |
| `mrctl audit [--limit N]` | `GET /audit` paginated | TSV or `--json` |

No write commands in v0.0.3. They land in ADR-0005 alongside the lifecycle endpoints.

### Observability surface this ADR adds

- **Traces:** every new endpoint inherits `WithServerSpan`; the new read-model calls emit child spans via `internal/observability/otel`'s helpers (`tracer.Start(ctx, "registry.envstate.get")` etc.).
- **Logs:** `registry.access` already fires per request; no new event names.
- **Metrics:** `registry_artifacts_total` gauge (current count from the substrate). Pre-registered as a `prometheus.GaugeFunc` that calls `len(items)` via a cheap `Count()` substrate method added in this iteration. No new counter metrics — read endpoints don't drive operator action counts.
- **Alerts:** none new in this ADR. The existing `RegistryHTTPErrorRateHigh` covers spikes on the read path the same way it covers the probe path.

### Performance bars (pre-registered, per ADR-0012 protocol)

| Bench | Target | Note |
|---|---|---|
| `BenchmarkGET_Artifacts_1000Items` | < 50 ms | wraps `BenchmarkStoreList_1000Artifacts_AllStates` (1.36 ms measured) with the chain + JSON marshal. |
| `BenchmarkGET_Artifact_ByHash` | < 5 ms | wraps `BenchmarkStoreGetBundle` (7.32 µs measured). |
| `BenchmarkGET_EnvState` | < 5 ms | new — per-env single-row read. |
| `BenchmarkGET_EnvHistory_100Entries` | < 50 ms | new — paginated history. |
| `BenchmarkGET_Audit_100Entries` | < 50 ms | new — paginated audit. |

The JSON marshal cost is the dominant additional factor over the substrate bars; the bars budget for it.

## Consequences

### Closed

- The read-only HTTP surface is locked: paths, query params, response shapes, error envelopes. Changes go through a follow-up ADR.
- The Reader-typed `Deps` fields are the operator-endpoint contract; write endpoints in ADR-0005 land on a parallel `Deps` slot, not by widening these.
- The env-state and audit read models live in separate SQLite files under `--store-root` (rejected: shared schema with `artifacts.db`). Substrate and read-model schemas evolve independently.
- `mrctl` is a thin HTTP client. It does not import `internal/store`; it talks to the wire.

### Not closed

- **Write endpoints** (ADR-0005+): `POST /upload`, `POST /promote`, `POST /rollback`, deployer with rolling push. The Writer projection of `envstate` and `audit` ships with that ADR; this ADR's typed contract is stable for it.
- **Auth on read endpoints**: v0.0.3 still assumes the registry runs in a trusted network. RBAC is a separate ADR.
- **Pagination scale**: cursor stability under concurrent writes is not exercised in this ADR — read-only traffic only. ADR-0005 revisits if mutation racing reads becomes observable.
- **The `audit.Reader.List` filter surface**: ADR-0004 ships unfiltered list-by-time; an `?operator=`, `?action=`, `?since=` query layer can land later without breaking the typed contract.

### Performance impact

Per-endpoint latency is dominated by the substrate call + JSON marshal. For the artifact substrate, the 1000-item scale measurement is `BenchmarkStoreList_1000Artifacts_AllStates` = 1.36 ms (over the 1 ms mark) and the 100-item scale is sub-millisecond by proportional extrapolation. The env-state substrate has no analogous measurement yet — `BenchmarkGET_EnvHistory_100Entries` pins it once the fsstate backing lands; the bar below already anticipates the bound. The chain overhead is the 1,310 ns ADR-0003 already measured. Total per-request budget on `/artifacts?limit=100`:

- chain overhead: ~1.3 µs
- substrate `List(Limit=100)` on fsstore: ~136 µs proportional + fixed SQLite query overhead (not subtracted in the substrate measurement). `BenchmarkGET_Artifacts_100Items` pins the real number when the bench lands.
- JSON marshal of 100 `ArtifactSummary`: ~50 µs (estimated; the same bench pins it).

The `< 50 ms` bar leaves two orders of magnitude of headroom even against the upper bound — defensible against regressions, not so tight it flakes under CI runner noise.

Boot-time cost: opening three SQLite files instead of one adds ~100 ms cold-boot. Total cold-boot budget stays under the ADR-0003 < 500 ms commitment.
