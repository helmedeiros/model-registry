# 1. Model Registry — control plane for versioned pricing rule artifacts

## Status

Accepted — the two-layer architecture (Versioned Config Store substrate + Model Registry service), the bre-go canonical format strategy (CSV authoritative; indexed JSON snapshot derived; Content-Type dispatch; no parser on the Registry's deploy path), and body-based reload as the markup-svc integration contract are the locked-in architectural decisions for this project. The package layout below — `internal/store`, `internal/registry/{state, gates, audit}`, `internal/deploy/markupsvc`, `internal/discovery/static`, `internal/httpapi`, `internal/observability` — governs subsequent ADRs. The cross-cutting observability + performance commitments are non-negotiable: every release ships with traces, structured logs, Prometheus metrics, AlertManager rules with runbooks, and pre-registered scientific harness bars.

## Context

The Pricing Decision Platform today operates rule sets as files on disk. Operators write CSV by hand or via CI; the file is bind-mounted (compose) or Kubernetes-ConfigMap'd (production) into the markup-svc container; `POST /admin/reload` re-reads the file in place. There is no versioned artifact store, no rollout state, no champion/challenger lifecycle, no audit trail for who promoted what when. Each operator and each CI pipeline runs its own conventions on top of an unstructured surface.

The pattern that fits this is well-established: a control plane that owns versioned model artifacts, tracks rollout state per environment, runs declarative promotion gates, and operates the existing data-plane admin surface programmatically. This ADR establishes the project that delivers it.

The two layers split along a clean boundary:

- **Versioned Config Store** — pure storage substrate. Content-addressed (SHA-256 hash) immutable artifacts with semver-tagged overlays. Treats every artifact as opaque bytes; does not vendor bre-go; does not parse formats.
- **Model Registry** — operational service on top of the Store. Tracks champion/challenger state per environment, runs promotion gates against Prometheus signals, orchestrates rolling and parallel pushes to running markup-svc instances, keeps the audit trail.

### The format strategy (no envelope)

A canonical artifact envelope was considered and rejected. The Registry stores artifacts in bre-go canonical formats:

- **`text/csv`** — the authoritative source. What operators author. What lives in Git. What humans diff.
- **`application/json`** — pre-compiled indexed snapshot (per markup-svc/ADR-0007). Derived from CSV by bre-go's `engine/indexed.ExportSnapshot`. Optional optimization for 100k+ rule sets where CSV parse + index build at reload time is expensive.

Both formats are owned by bre-go. The Registry stores opaque bytes and declares Content-Type. markup-svc dispatches on Content-Type to the appropriate bre-go parser. **The Registry never parses an artifact body in the deploy path** — that work is markup-svc's. An optional Registry-side parser linkage exists only for the upload-fallback compile path (when CI didn't pre-compile a snapshot and the Registry compiles one from the uploaded CSV); that is a discrete, isolated bre-go dependency scoped to the upload code path only, never reached on deploy. The deploy path stays format-blind. Future bre-go formats (e.g., a binary snapshot format, alternate JSON rule formats) would land as new Content-Type cases in markup-svc's dispatch switch — zero changes in the Registry's deploy path.

### Integration with markup-svc

The Registry's only data-plane dependency is markup-svc's `POST /admin/reload` accepting a non-empty body. That contract is the substrate-enabling change in markup-svc (its own ADR — markup-svc/ADR-0030 — body-based reload). Without that, the Registry has no programmatic deploy mechanism and falls back to file-on-disk + reload-trigger (slow, K8s-RBAC-heavy).

When body-based reload ships, the Registry deploys by HTTP POST with the artifact bytes in the body:

```
POST /admin/reload
  Content-Type: text/csv | application/json
  X-Model-Version: <hash or tag>
  Body: <CSV or snapshot bytes>
```

For champion changes: rolling push (one replica at a time, health-check between). Challenger changes (parallel-push, shadow-mode contract) are out of scope for this ADR.

### Cross-cutting requirements

Observability and performance discipline are non-negotiable from the first tagged release. This ADR commits the project to them at the contract level and defers concrete schemas (span naming, log event vocabulary, metric label sets, alert rule families with their pricing-observability runbook contracts, scientific harness protocol) to dedicated ADRs landing alongside the first substantive code commits: a per-cross-cutting ADR per concern, modeled on the markup-svc + pricing-observability pattern (markup-svc/ADR-0009 for OTel, ADR-0010 for metrics port, ADR-0021 for structured logs, ADR-0012 for scientific harness; pricing-observability/ADR-0008 for Prometheus alert rules). The first tag cut does not ship without all five concerns present and exercised.

## Decision

### Package layout

```
model-registry/
├── cmd/
│   ├── model-registry/             # the service binary
│   └── mrctl/                      # the operator CLI (mrctl = "model-registry-ctl";
│                                   # follows the <svc>ctl convention of kubectl)
├── internal/
│   ├── store/                      # Versioned Config Store substrate package
│   ├── registry/                   # composition root; thin coordinator over the three
│                                   # responsibilities split into subpackages below
│   │   ├── state/                  # FSM port: env state transitions (no side effects)
│   │   ├── gates/                  # promotion gate port (metrics.Source)
│   │   │   └── prom/               # adapter: Prometheus-backed metrics.Source
│   │   └── audit/                  # event sink port + struct types
│   ├── deploy/                     # deploy port (what registry calls to push artifacts)
│   │   └── markupsvc/              # adapter: POST /admin/reload over HTTP
│   ├── discovery/                  # instance discovery port
│   │   └── static/                 # adapter: list of base URLs per env
│   ├── httpapi/                    # operator HTTP surface (adapter)
│   └── observability/              # otel + metrics/prom + jsonlog wrappers
├── docs/
│   ├── architecture/decisions/     # ADRs
│   └── cookbook/                   # operator recipes
└── scientific/                     # performance harness per release
```

Hexagonal layering: `internal/store` is the storage substrate (domain). `internal/registry` is the composition root that wires three port subpackages (`state`, `gates`, `audit`) — each is a single-responsibility domain port, none knows about HTTP or markup-svc. `internal/deploy` and `internal/discovery` are ports with concrete adapters (`markupsvc/`, `static/`) so future deploy targets or discovery mechanisms (a Kubernetes-API discovery adapter, a Lambda deploy adapter for tests) land as siblings without renaming. `internal/httpapi` and `internal/observability` are leaf adapters. `cmd/*` is the composition layer.

### Two-layer split

- **`internal/store`** — typed Go API for PUT/GET/LIST/TAG operations. Backed by filesystem + SQLite for v1 (per the Desktop plan). Treats every artifact as `(bytes, content_type, metadata)`. Vendors nothing format-specific. Knows nothing about environments, champion/challenger, or operators.
- **`internal/registry`** — composition root over three single-responsibility subpackages. `state/` is the pure FSM (env state transitions; no side effects). `gates/` evaluates promotion criteria via a `metrics.Source` port; the Prometheus-backed adapter lives in `gates/prom/`, with in-memory fakes used by tests. `audit/` is the event sink port for state-transition records. The top-level `internal/registry` package is the coordinator that wires these three together with `internal/deploy` to actually push artifacts. Adding a new state, a new gate, or a new deploy mode lands as a small edit to one of the three packages — each with a single responsibility — instead of a large edit to a god package.

The Store can be reused by future services that need versioned artifact storage without the operational layer.

### Format dispatch (no parsers on the deploy path)

The Registry never parses an artifact body on the deploy path. On upload, the operator (or CI) declares `Content-Type`; the Registry stores it as metadata. On deploy, the Registry forwards the bytes + declared Content-Type to markup-svc. markup-svc's existing bre-go-backed parsers handle the actual work.

This means:
- New bre-go formats land in markup-svc and bre-go without touching the Registry's deploy code.
- The Registry's deploy path has no dependency on bre-go's API surface.
- The Registry's release cadence is governed by control-plane features, not data-plane format evolution.

The Registry's upload-fallback compile path (described under "Snapshot compilation responsibility" below) is the only place the Registry links bre-go's parsers. That linkage is isolated to one optional code path, never reached on deploy, and acknowledged explicitly so the "no parsers" framing stays honest.

### Snapshot compilation responsibility (layered)

Three places can compile a CSV into an indexed snapshot, layered by preference: CI is the primary compile site (reproducible, version-pinned, cached). The Registry is the fallback (compile-on-upload for ad-hoc operator uploads when CI isn't in the loop) — this is the only place the Registry links bre-go's parsers, and the linkage is scoped to the upload path only. markup-svc is the safety net (always able to parse CSV directly when no snapshot is available, via its existing parsers). The Registry's storage shape supports this: each artifact bundle holds `source.csv` (always) + `snapshot.json` (optional, when CI or the Registry-fallback uploaded the pair). The deploy path remains format-blind regardless of which layer compiled.

### Audit and lifecycle

Every state transition is captured in the audit log with `operator_identity`, `timestamp`, `reason`, `from_state`, `to_state`. Audit log is queryable via the operator API. Cross-platform forwarding to the pricing-observability Elasticsearch pipeline is out of scope for this ADR.

Artifact lifecycle: `uploaded → staged → champion / challenger → rolled-back / rejected → deprecated`. Deprecated artifacts remain in the Store for a retention period (compliance) before purge.

## Consequences

### Closed

- The platform gains a versioned artifact store + a champion/challenger lifecycle + a programmatic deploy mechanism for pricing rules.
- Operators stop bind-mounting CSVs and triggering reloads by hand. CLI-driven workflow with audit trail.
- The format-parsing surface on the Registry's deploy path is zero. bre-go remains the single source of truth for format awareness; new bre-go formats don't ripple into the Registry's deploy code. The optional upload-fallback compile linkage to bre-go is acknowledged, isolated, and never reached on deploy.
- The control plane / data plane split is honest: Registry orchestrates, markup-svc serves. Registry can be down without affecting Decide latency.
- **Observability is a release gate, not a goal.** Every iteration's tag cut blocks on traces + logs + metrics + alerts + pre-registered performance bars present and exercised by tests. An iteration that ships without any of the five does not ship.

### Not closed

- **Authentication.** v1 assumes Registry runs in trusted network; operators access via CLI on jumphost. Production-grade auth (OIDC, API keys, RBAC) is a separate iteration.
- **Web UI.** v1 is CLI + HTTP API. Web UI is a separate project.
- **Cross-region replication of the Store.** v1 is single-region.
- **Multi-environment promotion flow (dev → staging → production gates).** v1 has env as a label; orchestrating staged promotion is its own design.
- **Shadow models in markup-svc.** The Registry's challenger feature depends on markup-svc shadow Decider support, which is a separate markup-svc ADR.
- **Per-tenant routing at the gateway.** Orthogonal future capability.

### Performance impact

The Registry is a low-QPS control plane (operator-triggered actions; periodic state reconciliation). Performance budgets target operator UX, not customer-visible latency. The budget table below is **provisional pre-v0.0.1** — derived analytically against expected per-step cost, to be replaced with measured numbers in `scientific/v0.0.1/REPORT.md` per the markup-svc ADR-0012 protocol (pre-registered absolute bars, do not move post-commit).

Provisional budgets with derivation:

- `mrctl upload` for a 100-rule CSV (~10 KB): provisional < 200 ms end-to-end. Cost split: CLI startup (~30 ms) + HTTP round-trip on loopback (~5 ms) + SHA-256 of 10 KB (microseconds) + SQLite single-row insert (~1 ms) + filesystem fsync (~10 ms) = ~50 ms expected, with 4x headroom.
- `mrctl upload` for a 100k-rule CSV (~2 MB): provisional < 5 s end-to-end. Cost split: above + cached Diagnose run at upload time (markup-svc ADR-0025 measured the Diagnose pass at ~1 s per 100k rules on the indexed adapter) + fsync of 2 MB (~50 ms) = ~1.1 s expected, with 4x headroom for parser cold-cache and CI runner variance.
- `mrctl promote` rolling push to 3-replica markup-svc Deployment: provisional < 30 s end-to-end. Cost is bimodal by artifact size — for a 100-rule CSV the per-replica parse+swap is sub-100 ms; for a 100k-rule CSV the per-replica parse+swap is ~1 s per markup-svc ADR-0025's Diagnose measurement. Worst-case cost split (100k case): three sequential POSTs to `/admin/reload` (each ~100 ms body push + ~1 s markup-svc parse+swap + ~500 ms health-check wait per replica) = ~5 s expected, with 6x headroom for slow-network worst case. Typical-case cost (100 rules): ~2 s expected, deep under the bar.
- `mrctl state <env>` query: provisional < 50 ms. Cost split: HTTP round-trip + single SQLite SELECT + JSON encode = sub-ms expected; 50 ms is loose by design for a control-plane query.

The cmd binary's runtime overhead from observability hooks (one span per request + structured log emit + Prometheus counter increment) is provisional < 10 µs per operation. This tracks the platform's measured admin-path baselines: markup-svc/ADR-0009 measures noop-tracer overhead at ~100 ns; markup-svc/ADR-0028 measures real-tracer admin-span at 1–3 µs. The 10 µs budget gives 3–10x headroom over the measured tracer cost and 100x over the noop case — defensible against regressions, not so tight it flakes under CI runner noise.

The Store's per-artifact storage overhead: SHA-256 hash + metadata SQLite row (~200 bytes) + bytes on disk. At 1000 versions of a 100k-rule CSV (~2 MB each) the Store holds ~2 GB on disk — well within a single-node filesystem.
