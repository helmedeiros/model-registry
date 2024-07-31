# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.3] - 2024-07-31

### Added

- ADR-0004 Accepted: read-only operator endpoints + `cmd/mrctl` skeleton. Locks the read-only HTTP surface, the envstate + audit read-model packages with their conformance suites, and the CLI shape.
- `GET /artifacts` — paginated list of artifact summaries (limit / cursor / state query params; `ArtifactPage` envelope; invalid_limit / invalid_state / limit_too_large error envelopes). `BenchmarkGET_Artifacts_*` against memstore: 606 ns / hash bundle, 2 µs / 10 KB member.
- `GET /artifact/{hash}` — single bundle metadata returning `ArtifactBundle`; 404 not_found on unknown hash.
- `GET /artifact/{hash}/{member}` — raw byte stream for source / snapshot / diagnose; Content-Type = the artifact's declared type for source / `application/octet-stream` for derived members; X-Artifact-Hash echo header; 404 not_found / 404 member_absent / 400 invalid_member error envelopes.
- `GET /env/{env}/state` — current champion + challenger as nullable JSON roles; updated_at is an explicit `null` (not an absent field) on an unknown env so dashboards can scrape any env safely. `BenchmarkGET_EnvState` 559 ns/op.
- `GET /env/{env}/history` — newest-first paginated env transitions (same limit / cursor shape). `BenchmarkGET_EnvHistory_100Entries` 18 µs/op.
- `GET /audit` — newest-first paginated operator action log; `AuditPage` envelope with operator/action/target always-present, artifact_hash + reason genuinely optional. `BenchmarkGET_Audit_100Entries` 17.5 µs/op.
- `internal/envstate/` — typed Reader/Writer/Store contract per ADR-0004. State (env + nullable champion/challenger Roles + updated_at), Role (hash + promoted_by + promoted_at), Transition (env + Kind enum + from/to hash + operator + reason + at). DefaultListLimit + MaxListLimit pagination policy. `internal/envstate/memstate` ships the Reader projection (Get deep-copies Role pointers so a future Writer mutating in place cannot bleed through); Writer methods return `envstate.ErrNotImplemented` (wrapping `errors.ErrUnsupported`) until ADR-0005's lifecycle ships. `internal/envstate/envstatetest` is the reusable conformance suite (8 subtests covering empty env, seeded read, deep-copy isolation, history ordering, pagination, unknown-cursor restart, limit clamping, Writer stub).
- `internal/audit/` — typed Reader/Writer/Store contract. Entry (ULID id + operator + action + target + optional artifact hash + optional reason + at) — ID format documented so memaudit's sort tiebreaker on equal At is correct-by-contract. `internal/audit/memaudit` ships Reader with the same defensive-copy snapshot pattern as envstate; Writer returns `audit.ErrNotImplemented`. `internal/audit/audittest` is the reusable conformance suite (6 subtests). BenchmarkListSize_100Entries 2.4 µs/op, BenchmarkListSize_1000Entries 18.4 µs/op on the memaudit backing.
- `cmd/mrctl` — read-only operator CLI. Subcommands `artifacts`, `artifact <hash> [member]`, `state <env>`, `history <env>`, `audit`; `--registry` flag (default `http://localhost:8090`); `--json` flag for pretty output; default-30s HTTP client timeout. `doStream` caps member byte streams at 512 MB. The W3C TextMapPropagator is configured explicitly from `Run` (no `init()` side effect) so importing the package as a library does not silently mutate process state. Integration test boots an `httptest.Server` wrapping the real router, drives every subcommand through it, decodes the wire envelopes round-trip, and verifies the CLI's parent TraceID propagates to the server-side SpanKindServer span.
- `httpapi.Deps.Artifacts` / `Deps.EnvState` / `Deps.Audit` Reader-typed fields on the Router bundle — read-only at compile time. NewRouter validates via a single `validateDeps` loop that panics with a uniform message so a wiring miss fails fast at boot.

### Changed

- `internal/httpapi.NewRouter` mounts the five new ADR-0004 routes alongside the existing `/healthz` + `/readyz` + `/metrics`. Pattern wildcards (Go 1.22+ `ServeMux`) used for `/artifact/{hash}[/{member}]` and `/env/{env}/{state,history}`.
- `envstate.ErrNotImplemented` and `audit.ErrNotImplemented` wrap `errors.ErrUnsupported` so callers can detect a missing Writer projection via the stdlib sentinel without importing the package.
- Makefile `cover` filter now also excludes `envstatetest` and `audittest` from the coverage floor, same exception as `storetest`.

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
