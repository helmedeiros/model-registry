# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
