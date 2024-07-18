# 3. Service shell and observability bootstrap

## Status

Proposed — the boot sequence, the observability surface every operator-facing endpoint inherits, and the package layout that wires the `internal/store` substrate into `cmd/model-registry/main.go`. This ADR proposes; subsequent commits land the code that satisfies it and the status flips to Accepted when the substrate-only path is exercised by the end-to-end test against the live data plane.

## Context

ADR-0001 split the project into two layers (substrate + operational service) and committed to five non-negotiable cross-cutting concerns shipped from the first tag: OTel tracing, structured JSON logs, Prometheus metrics, AlertManager rules with runbooks, and a pre-registered scientific harness. ADR-0002 specified the typed substrate. The substrate is implemented and tested. The service shell is not yet wired — `cmd/model-registry/main.go` is empty.

This ADR is the contract for the shell. It does not yet expose any operator write surface (no `/upload`, no `/promote`, no `/rollback`); those land in ADR-0005 (champion deployment). What it does ship:

- Process boot — flag parsing, configuration, signal handling, graceful shutdown.
- Liveness / readiness — `/healthz` and `/readyz` with the same body shape the platform's other services use (`{status, reason?}`).
- Metrics — `/metrics` per the platform-standard ADR-0019 pattern (Prometheus textformat, `up`, `registry_http_*` from day one).
- Observability bootstrap — OTel tracer, jsonlog logger, Prometheus registry, all wired before any request handler runs.
- Substrate binding — the `internal/store` package wired to a configured filesystem root + the `fsstore` adapter, with `memstore` as the explicit alternative for in-process tests.

The shell ships without operator endpoints so the observability bootstrap, the boot sequence, and the substrate binding can be reviewed and locked in isolation. The first real operator endpoint (read-only state queries, ADR-0004) bolts onto the shell without changing its shape.

## Decision

### Process layout

```
cmd/model-registry/
├── main.go         — flag parse + cfg load + observability bootstrap +
│                     substrate open + http.Server + signal handling
└── (no other files; main.go stays small by delegating to internal/)

internal/
├── config/         — typed Config struct + flag/env loader
├── observability/
│   ├── otel/       — OTel tracer init + WithServerSpan middleware
│   ├── jsonlog/    — structured logger + WithAccessLog middleware +
│                     correlation-id + trace-id field plumbing
│   └── metrics/
│       └── prom/   — Prometheus registry + WithMetrics middleware +
│                     /metrics handler
├── httpapi/        — health + readyz handlers; route mounting;
│                     middleware composition order locked here
└── store/          — already implemented (ADR-0002)
```

`cmd/model-registry/main.go` does only composition: load config, build the observability stack, open the Store, build the router, register handlers, start the server, wait for SIGTERM/SIGINT, shut down cleanly. No business logic. Every internal package is independently testable.

### Boot sequence (deterministic order)

1. Parse flags + environment.
2. Initialise jsonlog at the configured level. From this point every emit lands in the structured stream.
3. Initialise the OTel tracer provider. Configure the exporter per config (no-op exporter in tests; OTLP in production).
4. Initialise the Prometheus registry + collectors.
5. Open the `fsstore` against the configured root. If `--store-backend=mem` is set (tests / smoke), bind `memstore` instead. The Store satisfies `store.Store`; the cmd layer never sees backing details.
6. Mount HTTP handlers: `/healthz`, `/readyz`, `/metrics`. Operator endpoints land in later ADRs.
7. Start the server. Log `registry.boot` with `attrs.{addr, store_backend, store_root, build_sha}`.
8. Block on signal. SIGTERM / SIGINT triggers graceful shutdown: stop accepting new connections, drain in-flight requests with a configurable timeout (default 10s), close the Store, log `registry.shutdown`.

Any failure in steps 1–6 logs `registry.boot.failed` with the cause and exits non-zero. The server does not start in a partially-initialised state.

### Observability surface (every endpoint inherits)

Middleware composition order, applied outermost to innermost:

```
WithRecover
  → WithCorrelationID            (mint or accept inbound)
    → WithTraceContext            (W3C extract / SpanKind=Server)
      → WithAccessLog             (emit registry.access)
        → WithMetrics             (registry_http_*)
          → handler
```

- **OTel** — every HTTP endpoint emits one `SpanKindServer` span via `WithServerSpan`. Span name `registry.http.<method>.<route>`. W3C `traceparent` extracted from inbound headers; injected on any outbound HTTP call (deploy, metrics scrape) the registry makes. Attributes per span: `registry.env`, `registry.outcome`, `registry.correlation_id`.
- **jsonlog** — the `registry.access` event per request with the standard HTTP-attr set (`method`, `path`, `status`, `duration_ms`, `correlation_id`, `trace_id`, `span_id`). Boot/shutdown events as above.
- **Prometheus** — `up{job="model-registry"}`, `registry_http_requests_total{method,path,status}`, `registry_http_request_duration_seconds{method,path}`. Exposed at `/metrics`.

### Configuration surface

The Config struct binds via flags (canonical) and env (12-factor convenience):

| Flag | Env | Default | Note |
|---|---|---|---|
| `--addr` | `REGISTRY_ADDR` | `:8090` | HTTP listener. |
| `--store-backend` | `REGISTRY_STORE_BACKEND` | `fs` | `fs` or `mem`. |
| `--store-root` | `REGISTRY_STORE_ROOT` | `./data` | Filesystem root for `fs` backend. |
| `--otel-exporter` | `REGISTRY_OTEL_EXPORTER` | `none` | `none` or `otlp`. |
| `--otel-endpoint` | `REGISTRY_OTEL_ENDPOINT` | `` | OTLP collector address; required when exporter=otlp. |
| `--log-level` | `REGISTRY_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |
| `--shutdown-timeout` | `REGISTRY_SHUTDOWN_TIMEOUT` | `10s` | Graceful drain budget. |

### Substrate binding

The cmd layer opens the substrate behind the `store.Store` interface. The fsstore root must exist or be creatable (fsstore.New takes care of `MkdirAll`). The memstore variant is for tests and smoke runs; explicitly opting in requires `--store-backend=mem` so a production miss-config that drops it to memory cannot happen silently.

The fsstore handle is closed during the shutdown sequence (between draining in-flight requests and exiting), guaranteeing the SQLite WAL is checkpointed and the file lock released before exit.

### Observability hooks on the substrate

The substrate (`internal/store`) is plain Go with no OTel instrumentation. The service shell wraps every operator-facing operation with the observability hooks listed above; the substrate calls themselves participate in the parent server span via context propagation. The cost of the wrapping is accounted for under `BenchmarkObservabilityOverhead` in the registry-package harness (< 100 µs per operation per ADR-0001), not under the Store bars committed in ADR-0002.

## Consequences

### Closed

- The shell's boot sequence and shutdown behaviour are locked. Adding new observability concerns (logs forwarding, push-based metrics) reuses the bootstrap; replacing one requires a follow-up ADR that supersedes this section.
- Middleware composition order is locked. Operator endpoints land between the existing middleware and the handler; they do not reshape the chain.
- The substrate is hidden behind `store.Store` at the composition site. Operator endpoints depend on the interface, never on `fsstore` directly.
- Healthz / readyz / metrics share the same observability stack as future operator endpoints. No special-case unobserved paths.
- The Config struct and its flag/env mapping is the operator's configuration contract for v0.0.2; changes go through an ADR.

### Not closed

- **Operator read endpoints** (ADR-0004): inspect environments, list artifacts, GET a bundle. The handlers land on top of this shell.
- **Operator write endpoints** (ADR-0005+): upload, promote, rollback. State machine and deployer live in later ADRs.
- **AlertManager rules** for `RegistryDown` and `RegistryHTTPErrorRateHigh` ship in the matching pricing-observability iteration, with runbooks under `pricing-observability/docs/runbooks/`. The wiring contract here (Prometheus textformat, exposed labels) is the interface the rules bind against.
- **Auth**: v0.0.2 assumes the registry runs in a trusted network. OIDC / API keys / RBAC is a separate ADR.

### Performance impact

The shell's per-request overhead, pre-registered against the platform's measured baselines:

| Hook | Provisional cost | Bar |
|---|---|---|
| `WithRecover` | ~50 ns (defer + recover with no panic) | < 1 µs |
| `WithCorrelationID` | ~100 ns (header read + uuid.NewRandom on miss) | < 5 µs |
| `WithTraceContext` | ~300 ns (W3C extract; no-op exporter path) | < 5 µs |
| `WithAccessLog` | ~1 µs (response wrapper + jsonlog marshal) | < 10 µs |
| `WithMetrics` | ~500 ns (counter inc + histogram observe) | < 5 µs |
| **Total inbound overhead** | ~2 µs expected | **< 100 µs** |

The < 100 µs total bar matches the model-registry roadmap's `BenchmarkObservabilityOverhead`. Sources for the per-hook costs: markup-svc/ADR-0009 measured noop-tracer overhead at ~100 ns; markup-svc/ADR-0021 measured jsonlog access events at ~800 ns; markup-svc/ADR-0010 measured Prometheus instrumentation at ~400 ns. The expected ~2 µs leaves 50× headroom against the bar — defensible against regressions, not so tight it flakes under CI runner noise.

Boot-time cost: substrate open (fsstore: `MkdirAll` + `sql.Open` + schema apply, ~50 ms on cold disk) + OTel init (~10 ms) + jsonlog init (~1 ms) + Prometheus init (~1 ms). Total cold-boot budget: < 500 ms. Hot-restart (existing root, warmed disk) is sub-100 ms.
