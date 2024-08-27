# Scientific measurement set — model-registry v0.0.4

v0.0.4 ships the champion lifecycle (POST /upload + /promote + /rollback) on top of three SQLite backings (fsstore + fsstate + fsaudit) under one `--store-root`. This report pins the v0.0.4 substrate + handler measurement set. Per the ADR-0012 protocol, every bar below is pre-registered before measurement; bars never move; honest framing.

## Pre-registered bars

| Benchmark | Bar | Layer | ADR reference | Status |
|-----------|-----|-------|---------------|--------|
| `BenchmarkBootTime_ThreeSQLiteFiles` | < 200 ms / op | substrate (cmd boot) | ADR-0005 §223 | implemented in this directory |
| `BenchmarkFsstateHistory_100Entries` | < 5 ms / op | substrate | ADR-0005 §158 (substrate micro-bar) | implemented at `internal/envstate/fsstate/fsstate_bench_test.go` |
| `BenchmarkFsstatePromoteChampion` | < 5 ms / op | substrate | ADR-0005 §158 | implemented at `internal/envstate/fsstate/fsstate_bench_test.go` |
| `BenchmarkFsstateRollbackChampion` | < 5 ms / op | substrate | ADR-0005 §158 | implemented at `internal/envstate/fsstate/fsstate_bench_test.go` |
| `BenchmarkFsauditList_100Entries` | < 5 ms / op | substrate | ADR-0005 §158 | implemented at `internal/audit/fsaudit/fsaudit_bench_test.go` |
| `BenchmarkFsauditRecord` | < 5 ms / op | substrate | ADR-0005 §158 | implemented at `internal/audit/fsaudit/fsaudit_bench_test.go` |
| `BenchmarkPOST_Upload_SmallArtifact` | < 200 ms / op | HTTP handler | ADR-0005 §191 | `internal/httpapi/upload_bench_test.go` |
| `BenchmarkPOST_Upload_LargeArtifact` | < 1 s / op | HTTP handler | ADR-0005 §192 | `internal/httpapi/upload_bench_test.go` |
| `BenchmarkPOST_Promote_3Replicas` | < 60 s / op | HTTP handler + rolling deployer (stubbed) | ADR-0005 §193 | `internal/httpapi/promote_bench_test.go` |
| `BenchmarkConcurrentOperatorAPI_10Concurrent` | < 500 ms p99 | HTTP handler under load | ADR-0005 §194 | `internal/httpapi/promote_bench_test.go` |

## Measured numbers — substrate (Apple M4, modernc.org/sqlite WAL)

These are the latest numbers from `make bench-substrate`. Numbers move freely; bars do not.

| Benchmark | Measured | Margin under bar |
|-----------|----------|------------------|
| `BenchmarkBootTime_ThreeSQLiteFiles` | 2.93 ms / op | ~68× |
| `BenchmarkFsstateHistory_100Entries` | 82 µs / op | ~60× |
| `BenchmarkFsstatePromoteChampion` | 67 µs / op | ~74× |
| `BenchmarkFsstateRollbackChampion` | 357 µs / op | ~14× |
| `BenchmarkFsauditList_100Entries` | 78 µs / op | ~60× |
| `BenchmarkFsauditRecord` | 42 µs / op | ~118× |

## Measured numbers — HTTP handler (Apple M4, stubbed deployer)

| Benchmark | Measured | Margin under bar |
|-----------|----------|------------------|
| `BenchmarkPOST_Upload_SmallArtifact` | 51 µs / op | ~3,900× (handler only; multipart parse + substrate Put at 1 KB) |
| `BenchmarkPOST_Upload_LargeArtifact` | 2.18 ms / op | ~459× (handler only; multipart parse + substrate Put at 2 MB) |
| `BenchmarkPOST_Promote_3Replicas` | 55 µs / op | N/A — stub deployer returns instantly. The 60 s bar covers the real rolling-push wall-clock (3 × 10 s per-instance `/readyz` timeout). This bench validates handler dispatch only; the bar is validated by the ADR-0005.x live-stack gate. |
| `BenchmarkConcurrentOperatorAPI_10Concurrent` | 3.99 ms p99 | ~125× (10 distinct envs; WAL-serialised contention + lifecycle spans dominate the round wall-clock at ~2.14 ms — still under the analytic "< 10 ms contention" the ADR pre-registered) |

**Observability cost**: numbers above are measured AFTER the post-v0.0.4 observability chunks landed (lifecycle child spans + log trace correlation + audit TraceID + deploy-duration exemplar). Each handler call now pays for 2–4 span Start/End cycles + exemplar lookup; the per-call cost roughly doubled vs the pre-observability numbers but every bar still clears with 125–3,900× margin. The biggest relative hit is on the concurrent path (p99 went from 595 µs to 3.99 ms — 6.7×) because the 10 parallel goroutines each pay the new per-span allocation.

The handler bars cover the operator-visible round-trip including the deployer's per-instance push to a real markup-svc. The benches above stub the deployer (the rolling package's own benches measure the per-instance cost) so the measured numbers are handler-only and the margin column for the deploy-bound bars is "N/A". The live-stack E2E for the full round-trip is the ADR-0005.x gate, not a bar in this set.

## Method

- Build tag `bench` keeps these out of the default `make test` run.
- Each substrate bench uses `b.TempDir()` for a fresh SQLite file inside the timed loop where applicable, or as setup outside the loop where the goal is steady-state throughput.
- `BenchmarkBootTime_ThreeSQLiteFiles` rotates the store-root inside the loop so every iteration is a cold open. The defers' Close calls run with the timer stopped so the bench measures only the schema-bootstrap path.
- Cmd-shell `TestRun_FSBackendOpensThreeSQLiteFiles` (`cmd/model-registry/main_test.go`) provides the integration-level proof that the same three files appear under `--store-root` when the binary boots.

## Live-stack proof

`scientific/v0.0.4/e2e_lifecycle_test.go` is the executable proof of the ADR-0005 §Status revision gate. Build-tagged `e2e`; operators run with `make e2e` against a markup-svc reachable at `MARKUP_SVC_URL` (default `http://localhost:8080`). The test boots the full model-registry in-process (memstore + memstate + memaudit + real rolling deployer + static discovery), drives the operator round-trip end-to-end:

  POST /upload csvA → hashA
  POST /promote {hashA, production} → rolling deploy to markup-svc /admin/reload
  POST /decide (markup-svc) → asserts csvA rule fires
  POST /upload csvB → hashB
  POST /promote {hashB, production} → rolling deploy
  POST /decide → asserts csvB rule fires
  POST /rollback → rolling deploy back to csvA
  POST /decide → asserts csvA rule fires again
  GET /audit → asserts the five transitions land newest-first

The test verifies (a) the rolling deployer pushes the real bytes the substrate stored, (b) the deployer talks the body-based reload contract markup-svc/ADR-0030 specifies, (c) the envstate transitions and audit entries land in lockstep with the deploys, and (d) the rollback contract restores the prior champion in both the registry's read-model and the data plane.
