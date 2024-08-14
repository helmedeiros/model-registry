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
| `BenchmarkPOST_Upload_SmallArtifact` | < 200 ms / op | HTTP handler | ADR-0005 §191 | pending |
| `BenchmarkPOST_Upload_LargeArtifact` | < 1 s / op | HTTP handler | ADR-0005 §192 | pending |
| `BenchmarkPOST_Promote_3Replicas` | < 60 s / op | HTTP handler + rolling deployer | ADR-0005 §193 | pending |
| `BenchmarkConcurrentOperatorAPI_10Concurrent` | < 500 ms p99 | HTTP handler under load | ADR-0005 §194 | pending |

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

## Method

- Build tag `bench` keeps these out of the default `make test` run.
- Each substrate bench uses `b.TempDir()` for a fresh SQLite file inside the timed loop where applicable, or as setup outside the loop where the goal is steady-state throughput.
- `BenchmarkBootTime_ThreeSQLiteFiles` rotates the store-root inside the loop so every iteration is a cold open. The defers' Close calls run with the timer stopped so the bench measures only the schema-bootstrap path.
- Cmd-shell `TestRun_FSBackendOpensThreeSQLiteFiles` (`cmd/model-registry/main_test.go`) provides the integration-level proof that the same three files appear under `--store-root` when the binary boots.

## Outstanding pre-registered work

- HTTP-layer benches (4 entries marked pending above). Each will live alongside the handler it exercises (`internal/httpapi/upload_bench_test.go`, `promote_bench_test.go`, etc.) rather than in `scientific/` so the bench failure surfaces next to the code it bars.
- Live-stack E2E (registry + markup-svc compose) — the ADR-0005.x revision gate; not a bar in this set, but the next-chunk integration proof.
