# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.2] - 2024-07-25

### Added

- ADR-0003 Accepted: service shell + observability bootstrap. Locks the boot sequence, the middleware composition order, the configuration surface, and the substrate-binding contract.
- `internal/observability/jsonlog/` — structured-log package emitting the platform shape `{time, level, msg, attrs}`. Levels (Debug/Info/Warn/Error) filter at emit time; below-threshold events allocate nothing. `ParseLevel` errors on unknown values so a typo'd `--log-level` surfaces at boot. `json.Marshal` runs outside the lock so reflect-driven map iteration does not stall concurrent emitters.
- `internal/observability/metrics/prom/` — Prometheus adapter exposing `registry_http_requests_total{method,path,status}` (counter) and `registry_http_request_duration_seconds{method,path}` (histogram) against a private `*prometheus.Registry`. `BenchmarkRecordRequest` reads 94.5 ns/op on Apple M4 — inside the < 500 ns budget with ~5x headroom. Pinned at `github.com/prometheus/client_golang v1.14.0` matching the markup-svc revision in production.
- `internal/observability/otel/` — Bootstrap returns a Tracer + Shutdown for the configured exporter (`none` no-op default or `otlp` gRPC). `WithServerSpan` middleware opens one `SpanKindServer` span per request named `registry.http.<METHOD>.<route>`, extracts W3C `traceparent` from inbound headers, marks 5xx as Error span status. `BenchmarkServerSpan_NoopTracer` reads 251.9 ns/op — inside ADR-0003's < 300 ns budget. Pinned at `go.opentelemetry.io/otel v1.11.2`.
- `internal/httpapi/` — the operator-facing HTTP surface: `Healthz` / `Readyz` probes with the platform `{status, reason?}` body shape; `WithCorrelationID` middleware accepting or minting an X-Correlation-ID v4 UUID (encoded directly into a fixed `[36]byte` so the minting path stays a single allocation); `WithRecover` middleware catching panics into `registry.panic` via a one-method `PanicSink` interface; `WithAccessLog` middleware emitting `registry.access` with method/path/status/duration_ms + correlation_id + trace_id/span_id; `WithMetrics` middleware calling `prom.RecordRequest(method, route, status, duration)`; `NewRouter(Deps, metricsHandler)` composing the ADR-locked chain (Recover → CorrelationID → ServerSpan → AccessLog → Metrics → handler). `BenchmarkRouterChain_NoopTracer` reads 1,310 ns/op for the full chain — comfortably inside ADR-0003's < 100 µs total inbound overhead bar with ~75x headroom.
- `internal/config/` — typed `Config` struct + `LoadFromArgs` accepting `--flag` (canonical) layered on `REGISTRY_*` env (12-factor convenience). Unknown values for `--store-backend` and `--otel-exporter` error at boot rather than silently defaulting. Defaults follow ADR-0003: `:8090`, `fs`, `./data`, `none`, `info`, `10s`.
- `cmd/model-registry/main.go` — the testable boot entrypoint `Run(ctx, args, stdout, stderr, listener)`. Boot sequence: parse → jsonlog → OTel → Prometheus → Store (`fsstore` or `memstore`) → router → `http.Server` → `signal.NotifyContext` → graceful shutdown. The optional `net.Listener` parameter is the seam tests use to bind `:0` and learn the port. The readiness closure issues a `Limit:1` `List` against the store on every `/readyz` probe so a handle that went bad after open does not pass silently. Integration test boots the server end-to-end and asserts the full chain wires.

### Changed

- `internal/store/` exports shared pagination policy constants `DefaultListLimit` and `MaxListLimit` (was duplicated per backing in v0.0.1; hoisted so future backings cannot silently diverge).

## [0.0.1] - 2024-07-22

### Added

- ADR-0002: Versioned Config Store substrate — typed Go API (`Reader`/`Writer`/`Store`), content-addressed storage, filesystem + SQLite backing, lifecycle states (Staged → Active → Deprecated, terminal).
- `internal/store/` — typed substrate package: `Hash`, `ContentType`, `MemberKind`, `State`, `Metadata`, `PutRequest`, `Bundle`, `Summary`, `ListOptions`, `Page`, plus shared pagination policy `DefaultListLimit=100` / `MaxListLimit=1000`. Error sentinels: `ErrNotFound`, `ErrTagUnknown`, `ErrMemberAbsent`, `ErrInvalidKind`, `ErrInvalidTransition`, `ErrCorrupt`, `ErrSourceRequired`, `ErrContentTypeRequired`.
- `internal/store/memstore/` — in-memory `Store` backing used by tests and registry harness. Full lifecycle, idempotent `Put`, deterministic ordering via injectable clock.
- `internal/store/storetest/` — reusable conformance suite: `RunConformance(t, factory)` exercises every behaviour the `store.Store` contract promises. Both memstore and fsstore run the same 23 subtests from this single source of truth.
- `internal/store/fsstore/` — filesystem + SQLite implementation. Schema bootstrapped via DSN `_pragma` parameters (WAL, synchronous=FULL, busy_timeout=5s, foreign_keys=ON) so every pool connection inherits the configuration. `Put` writes via tempfile + fsync + rename and uses `INSERT OR IGNORE` for race resolution. `Tag` transitions Staged → Active inside a transaction; `ResolveTag` runs read-only. `ListTags` uses a `ROW_NUMBER()` window for head extraction. `Deprecate` is the terminal state transition; tagging a deprecated artifact errors with `ErrInvalidTransition`. `List` paginates by `(created_at DESC, hash ASC)` with an exclusive cursor, fetches `limit+1` rows to detect a next page without a second query, and rides `idx_artifacts_state_created` for the state-filtered path.
- `scientific/v0.0.1/` — pre-registered evaluation bars per the markup-svc/ADR-0012 protocol. 12 substrate micro-bars (Put × 3 / GetBundle / GetMember × 2 / Tag / ResolveTag / ListTags / Deprecate / List × 2) plus the end-to-end `TestE2EBodyPush_RoundTrip` against a live markup-svc. v0.0.1 measurement set locked on Apple M4 / NVMe with every bar holding inside its committed margin.
- `make e2e` target — runs the build-tagged `e2e` harness against a live markup-svc (default `http://localhost:8080`).

### Changed

- Module Go version bumped from 1.18 → 1.25 (toolchain pinned) to accept the `modernc.org/sqlite` driver.
- CI workflow on Node 24 actions (`actions/checkout@v5`, `actions/setup-go@v6`) with `cache: true` so the module cache populates from `go.sum`.

### Internal

- ADR hygiene script (`scripts/check-adrs.sh`) verifies ADR index + status + four-section structure on every CI run.
- Coverage floor scoped to substantive packages; `cmd/*` and `storetest` excluded from the threshold computation but still vetted and tested.
