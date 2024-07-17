# scientific / v0.0.1 — Versioned Config Store substrate

Pre-registered evaluation bars per the markup-svc/ADR-0012 protocol. Bars are committed in this file BEFORE measurement; bars do not move post-commit. Measured numbers are filled in only after the run.

Status: pre-registration in progress. Substrate land in [a56ad71..be990b9]; benches scheduled to run before tagging `model-registry v0.0.1`.

## Pre-registered bars

### Store substrate micro-bars (per ADR-0002)

Run against `internal/store/fsstore` directly (not through the registry layer). Storage tier: NVMe SSD with `_journal_mode=WAL`, `_synchronous=FULL`, `_busy_timeout=5000`. Bars assume single-process single-writer per ADR-0002.

| Bench | Bar | Status |
|---|---|---|
| `BenchmarkStorePut_SmallArtifact` (10 KB source only) | < 15 ms | pre-registered |
| `BenchmarkStorePut_SmallArtifact_AllMembers` (10 KB source + snapshot + diagnose) | < 15 ms | pre-registered |
| `BenchmarkStorePut_LargeArtifact` (~2 MB source only) | < 200 ms | pre-registered |
| `BenchmarkStoreGetBundle` (metadata only) | < 5 ms | pre-registered |
| `BenchmarkStoreGetMember_SmallSourceWarm` (≤ 100 KB warm) | < 5 ms | pre-registered |
| `BenchmarkStoreGetMember_LargeSourceCold` (multi-MB cold) | < 50 ms | pre-registered |
| `BenchmarkStoreList_1000Artifacts_StateFiltered` | < 50 ms | pre-registered |
| `BenchmarkStoreList_1000Artifacts_AllStates` | < 50 ms | pre-registered |
| `BenchmarkStoreTag` | < 10 ms | pre-registered |
| `BenchmarkStoreResolveTag` | < 5 ms | pre-registered |
| `BenchmarkStoreListTags_1000Tags` (window-function head extraction) | < 10 ms | pre-registered |
| `BenchmarkStoreDeprecate` | < 10 ms | pre-registered |

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

## Measured numbers (filled in post-run)

To be added once the harness runs.

---

References:
- markup-svc/ADR-0012 — scientific harness protocol (bars pre-registered, do not move post-commit).
- ADR-0001, ADR-0002 — substrate architecture and committed bars.
