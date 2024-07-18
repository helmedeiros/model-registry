# scientific / v0.0.1 — Versioned Config Store substrate

Pre-registered evaluation bars per the markup-svc/ADR-0012 protocol. Bars are committed in this file BEFORE measurement; bars do not move post-commit. Measured numbers are filled in only after the run.

Status: bars pre-registered, micro-benches run on Apple M4 / NVMe SSD with `_journal_mode=WAL`, `_synchronous=FULL`, `_busy_timeout=5000`. The two not-yet-implemented benches (`BenchmarkStoreList_*`, `BenchmarkStoreDeprecate`) land with fsstore chunks (d) + (e). E2E round-trip green.

## Pre-registered bars

### Store substrate micro-bars (per ADR-0002)

Run against `internal/store/fsstore` directly (not through the registry layer). Storage tier: NVMe SSD with `_journal_mode=WAL`, `_synchronous=FULL`, `_busy_timeout=5000`. Bars assume single-process single-writer per ADR-0002.

| Bench | Bar | Status |
|---|---|---|
| `BenchmarkStorePut_SmallArtifact` (10 KB source only) | < 15 ms | pre-registered |
| `BenchmarkStorePut_SmallArtifact_AllMembers` (10 KB source + snapshot + diagnose) | < 25 ms | pre-registered (split from the source-only bar; ADR-0002 derivation expected ~10–14 ms for 3-fsync case) |
| `BenchmarkStorePut_LargeArtifact` (~2 MB source only) | < 200 ms | pre-registered |
| `BenchmarkStoreGetBundle` (metadata only) | < 5 ms | pre-registered |
| `BenchmarkStoreGetMember_SmallSourceWarm` (≤ 100 KB warm) | < 5 ms | pre-registered |
| `BenchmarkStoreGetMember_LargeSource` (~2 MB warm cache) | < 50 ms | pre-registered |
| `BenchmarkStoreList_1000Artifacts_StateFiltered` | < 50 ms | pre-registered (lands with chunk e) |
| `BenchmarkStoreList_1000Artifacts_AllStates` | < 50 ms | pre-registered (lands with chunk e) |
| `BenchmarkStoreTag` | < 10 ms | pre-registered |
| `BenchmarkStoreResolveTag` | < 5 ms | pre-registered |
| `BenchmarkStoreListTags_1000Tags` (window-function head extraction) | < 10 ms | pre-registered |
| `BenchmarkStoreDeprecate` | < 10 ms | pre-registered (lands with chunk d) |

### End-to-end capability bar (Iteration 1 + 2 composition)

The user-facing capability committed for v0.0.1: "operators can push a model artifact through the Registry substrate and observe a markup-svc `/decide` response reflecting the new model." This bar exercises the composition end-to-end:

1. fsstore.Put a CSV.
2. fsstore.GetMember(MemberSource) returns identical bytes.
3. HTTP POST those bytes to `markup-svc /admin/reload` with `Content-Type: text/csv`.
4. HTTP POST a representative request body to `markup-svc /decide`.
5. Assert: `/decide` response's `markup_factor` matches the rule pushed in step 1.

| Test | Bar | Status |
|---|---|---|
| `TestE2EBodyPush_RoundTrip` (substrate→markup-svc→decide) | PASS; total wall < 250 ms on the dev compose stack (NVMe SSD, localhost). | pre-registered |

This test runs under build tag `e2e` so the default `go test ./...` does not require a live data plane. CI invokes it via `make e2e` when a markup-svc URL is configured; local runs use the compose default `http://localhost:8080`.

### Regression posture

Every commit on `model-registry/main` keeps the data-plane smoke green (Iteration 1's body-based reload + `/decide` reflecting the swap). When a substrate commit lands, the E2E test re-runs against the same compose stack — a regression in the substrate contract (atomic file IO, hash stability, ContentType propagation) surfaces as an E2E failure the same session it is introduced.

## Measured numbers

Hardware: Apple M4 (10 cores, arm64, macOS), NVMe-class SSD via APFS. Substrate pragmas as committed: WAL + synchronous=FULL + busy_timeout=5s + foreign_keys=ON. `go test -tags=bench -benchtime=1s ./scientific/...`

| Bench | Bar | Measured (ns/op) | Result | Headroom |
|---|---|---|---|---|
| `BenchmarkStorePut_SmallArtifact` | < 15 ms | 9.26 ms | PASS | ~38% |
| `BenchmarkStorePut_SmallArtifact_AllMembers` | < 25 ms | 17.75 ms | PASS | ~29% |
| `BenchmarkStorePut_LargeArtifact` | < 200 ms | 10.85 ms | PASS | ~95% |
| `BenchmarkStoreGetBundle` | < 5 ms | 7.46 µs | PASS | 3 orders of magnitude under |
| `BenchmarkStoreGetMember_SmallSourceWarm` | < 5 ms | 18.60 µs | PASS | 2 orders under |
| `BenchmarkStoreGetMember_LargeSource` | < 50 ms | 192.91 µs | PASS | 2 orders under |
| `BenchmarkStoreTag` | < 10 ms | 78.69 µs | PASS | 2 orders under |
| `BenchmarkStoreResolveTag` | < 5 ms | 7.23 µs | PASS | 2 orders under |
| `BenchmarkStoreListTags_1000Tags` | < 10 ms | 1.09 ms | PASS | ~89% |

Allocation profile: GetBundle 1.5 KB / 52 allocs; GetMember small 12.5 KB / 31 allocs; GetMember large 2.1 MB / 31 allocs (dominated by the cloned source slice — ADR-0002 isolation invariant). All bars hold with comfortable margin.

### End-to-end run

`make e2e` → `TestE2EBodyPush_RoundTrip` against markup-svc:v0.1.21 (compose stack, localhost): **PASS at 15.84 ms (bar 250 ms).**

## Open items before tagging v0.0.1

- Implement `Deprecate` (chunk d) → unlocks `BenchmarkStoreDeprecate`.
- Implement `List` (chunk e) → unlocks `BenchmarkStoreList_*`.
- Wire `fsstore_test.TestConformance` (chunk f) → contract suite proves fsstore satisfies the typed Store.
- Re-run all benches on the same hardware to lock the v0.0.1 measurement set.

---

References:
- markup-svc/ADR-0012 — scientific harness protocol (bars pre-registered, do not move post-commit).
- ADR-0001, ADR-0002 — substrate architecture and committed bars.
- ADR-0003 — service shell and observability bootstrap (proposed).
