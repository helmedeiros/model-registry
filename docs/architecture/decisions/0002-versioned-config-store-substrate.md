# 2. Versioned Config Store — typed Go substrate

## Status

Accepted — the typed Go API surface, content-addressed storage semantics, filesystem + SQLite backing, and the artifact lifecycle states defined below are the locked-in substrate decisions for `internal/store`. Subsequent ADRs (the service shell, the operator HTTP surface, the registry state machine) build on this surface; they do not reshape it.

This ADR is intentionally narrow. It scopes only the Store package — the typed Go API and the storage shape behind it. The HTTP layer that exposes the Store to operators, the OTel + jsonlog + Prometheus bootstrap that wraps the service, and the registry-side state machine all land in their own ADRs.

## Context

ADR-0001 split the project along a clean boundary: the Versioned Config Store is the storage substrate; the Model Registry is the operational layer on top. The Store treats every artifact as opaque bytes with a declared `Content-Type`; the Registry tracks champion/challenger state, runs gates, and orchestrates deploys. This ADR commits the Store's surface.

The substrate has to deliver four properties at once:

- **Immutable artifacts, mutable references.** Operators reason about pricing-rule versions as moving labels (`v4`, `staging-candidate`, `production`); auditors reason about them as fixed hashes (`sha256:abc…`). Content-addressed storage gives auditors the immutable proof; tag overlays give operators the human-readable handle.
- **Format-blind storage.** Per ADR-0001, the Store stores bytes plus `Content-Type`. It does not vendor bre-go, does not parse CSV, does not parse snapshots, does not understand rules. New bre-go formats land without touching this package.
- **Single-process, single-writer simplicity.** The Registry is one Go process. There is no clustering, no leader election, no distributed coordination in scope for v1. The Store inherits this simplicity: one writer, one filesystem root, one SQLite file.
- **Backup is a tarball.** Operators copy `<root>/` to recover. The artifact files on disk are the source of truth; SQLite is a derived index that can be rebuilt by walking the object tree if it is lost.

A bundle per artifact, not a single blob. ADR-0001 defined four files per artifact:

```
artifact <hash>:
  source.csv          ← authoritative bre-go CSV (always present)
  snapshot.json       ← optional pre-compiled indexed snapshot
  diagnose.json       ← cached Diagnose result against source.csv at upload time
  metadata.json       ← created_at, created_by, source_commit_sha, description, derived_by_version
```

The Store has to serve each of these independently: a deploy might fetch only `source.csv`; an operator inspecting an artifact might read `metadata.json`; the deploy fast-path fetches `snapshot.json` if present. Returning the bundle as one blob would force every consumer to download bytes it does not need; serving them as four addressable entities under one hash keeps the operations cheap.

The lifecycle. An artifact is `staged` on upload (immutable, indexed, never selected for any env). It becomes `active` when a tag points at it or the Registry references it as a champion or challenger. It becomes `deprecated` when explicitly soft-deleted; deprecated artifacts remain on disk for the compliance retention period before a separate purge job runs (the purge mechanism is out of scope here). The Store enforces these transitions so the Registry can say "deprecate this version" without losing audit history. The Store does not know what `champion` or `challenger` means; only the registry does. The Store knows that something has a tag, or has been deprecated.

The SQLite index. Hashes give immutability, but operators query the Store along three dimensions the filesystem cannot answer in constant time: "what is the latest artifact?", "what does tag `v4` resolve to today?", "is this hash deprecated?". SQLite is the smallest backing that answers all three with the right semantics: ACID transactions for tag re-pointing, a single file that copies cleanly with the object tree, sub-millisecond steady-state reads on a warm page cache (cold-start reads are 1–5 ms until the kernel cache populates) with no daemon. Migrating to Postgres later is an internal-to-the-package concern; the typed Go surface this ADR commits does not depend on the backing.

## Decision

### Typed Go API surface

```go
package store

// Hash is a content-address. Stored as the hex-encoded SHA-256 of source bytes.
type Hash string

// ContentType is the declared media type of an artifact's source bytes.
// The Store does not validate that bytes actually match the declared type;
// that is the consumer's responsibility (per ADR-0001's format-blind framing).
type ContentType string

const (
    ContentTypeCSV      ContentType = "text/csv"
    ContentTypeSnapshot ContentType = "application/json"
    // ContentTypeUnknown is the sentinel returned by GetMember for derived
    // members (snapshot, diagnose). The substrate does not assert a media
    // type for derived bytes; callers branch on MemberKind, not ContentType.
    ContentTypeUnknown  ContentType = ""
)

// State is the artifact's lifecycle state. Transitions are linear:
// staged -> active -> deprecated. There is no path back from deprecated
// (terminal for v1, compliance-retention rationale; see Decision below).
type State string

const (
    StateStaged     State = "staged"
    StateActive     State = "active"
    StateDeprecated State = "deprecated"
)

// MemberKind enumerates the addressable members of an artifact bundle.
// Adding a new derived format (e.g. a binary snapshot) lands as a new
// constant, not as a new interface method — keeping the substrate
// format-blind at the port level.
type MemberKind string

const (
    MemberSource   MemberKind = "source"   // the authoritative bytes (required)
    MemberSnapshot MemberKind = "snapshot" // optional derived pre-compiled form
    MemberDiagnose MemberKind = "diagnose" // optional cached validation result
)

// Metadata is the operator-facing description of an artifact bundle.
// Stored as metadata.json on disk and as columns on the artifacts SQLite row.
// DerivedByVersion identifies the producer of any derived members (e.g.,
// the toolchain version that compiled snapshot from source); the substrate
// stores the value without interpretation.
type Metadata struct {
    CreatedAt        time.Time `json:"created_at"`
    CreatedBy        string    `json:"created_by"`
    SourceCommitSHA  string    `json:"source_commit_sha,omitempty"`
    Description      string    `json:"description,omitempty"`
    DerivedByVersion string    `json:"derived_by_version,omitempty"`
}

// PutRequest carries the bytes and metadata for a new artifact.
// SnapshotBytes and DiagnoseBytes are optional; SourceBytes and ContentType
// are required. On a Put for an already-existing hash, SnapshotBytes,
// DiagnoseBytes, and Metadata in the request are ignored — the hash is the
// canonical identity of the source bytes only.
type PutRequest struct {
    SourceBytes   []byte
    ContentType   ContentType
    SnapshotBytes []byte
    DiagnoseBytes []byte
    Metadata      Metadata
}

// Bundle is the metadata-only projection of an artifact. The bytes for
// any member are fetched separately via GetMember — keeping GetBundle
// cheap and explicit about what callers pay for.
type Bundle struct {
    Hash         Hash
    ContentType  ContentType
    Metadata     Metadata
    State        State
    HasSnapshot  bool
    HasDiagnose  bool
}

// Summary is the cheap-to-list projection of an artifact (no body bytes).
type Summary struct {
    Hash        Hash
    ContentType ContentType
    Metadata    Metadata
    State       State
}

// ListOptions paginate the artifact list. Cursor is opaque to callers.
type ListOptions struct {
    Limit  int    // max items returned (Store-enforced ceiling)
    Cursor string // opaque pagination handle from a previous Page
    State  State  // optional filter; empty means any state
}

// Page is one slice of a list traversal.
type Page struct {
    Items      []Summary
    NextCursor string // empty when the list is exhausted
}

// Reader is the read-only projection of the Store. Callers that do not
// need to mutate (HTTP read endpoints, deploy fast-path) depend on Reader
// to keep their dependency surface honest and test doubles cheap.
type Reader interface {
    // GetBundle returns the metadata-only bundle for a hash. Returns
    // ErrNotFound when the hash is unknown. No body bytes are loaded.
    GetBundle(ctx context.Context, h Hash) (Bundle, error)

    // GetMember returns the bytes of a single bundle member. Returns
    // ErrNotFound when the hash is unknown; ErrMemberAbsent when the
    // hash exists but the requested member was never uploaded
    // (snapshot / diagnose). MemberSource always exists for a
    // known hash. The returned ContentType is the artifact's declared
    // ContentType for MemberSource and ContentTypeUnknown for derived
    // members; callers branch on MemberKind, not on the returned
    // ContentType, to decide how to interpret the bytes.
    GetMember(ctx context.Context, h Hash, m MemberKind) ([]byte, ContentType, error)

    // List paginates artifact summaries. Order is created_at descending.
    List(ctx context.Context, opts ListOptions) (Page, error)

    // ResolveTag returns the current hash for a tag. Returns ErrTagUnknown
    // when the tag has never been assigned.
    ResolveTag(ctx context.Context, tag string) (Hash, error)

    // ListTags returns all current tag-to-hash mappings (heads only).
    // Tag history is recorded in the underlying SQLite table for audit
    // queries but is not surfaced through the Reader interface in v1;
    // operator audit queries hit a separate read model.
    ListTags(ctx context.Context) (map[string]Hash, error)
}

// Writer is the mutating projection of the Store. The registry coordinator
// is the only in-process caller that needs Writer — the HTTP API and the
// deploy hot-path are Reader-only.
type Writer interface {
    // Put writes a new artifact bundle and returns the assigned hash.
    // Hash is sha256(SourceBytes). Repeated Put of the same source bytes
    // is idempotent: the existing bundle's hash is returned and the
    // request's SnapshotBytes / DiagnoseBytes / Metadata are ignored
    // (the hash is the canonical identity of the source bytes only).
    // State on the first successful Put is Staged. Tag implicitly
    // transitions Staged -> Active as a side effect of pointing a tag
    // at a hash; there is no separate Activate method on the substrate
    // (the registry layer owns any non-tag-driven activation in its own
    // state model).
    Put(ctx context.Context, req PutRequest) (Hash, error)

    // Tag points a human-readable label at a hash. Idempotent; re-pointing
    // is allowed and recorded as an append-only tag history row. The Store
    // transitions the target hash from Staged to Active if it was Staged.
    // Returns ErrNotFound when the hash is unknown; ErrInvalidTransition
    // when the target hash is Deprecated.
    Tag(ctx context.Context, tag string, h Hash) error

    // Deprecate marks an artifact deprecated. Terminal for v1 — the
    // ADR's compliance-retention posture forecloses re-activation.
    // Returns ErrInvalidTransition when the artifact is already
    // deprecated. The Store does not enforce that the registry has
    // unreferenced the artifact first; that is the registry's job.
    Deprecate(ctx context.Context, h Hash, reason string) error
}

// Store is the union of Reader and Writer for callers (the registry
// coordinator, the fsstore / memstore adapters) that need both.
type Store interface {
    Reader
    Writer
}
```

Error sentinels: `ErrNotFound`, `ErrTagUnknown`, `ErrMemberAbsent`, `ErrInvalidTransition`, `ErrCorrupt` (filesystem and SQLite disagree on what exists).

The interface is the contract. The filesystem + SQLite implementation behind it lives in `internal/store/fsstore` (a sibling package). Tests against `internal/registry` use an in-memory `internal/store/memstore` adapter that satisfies the same interface — keeps fast tests fast and lets the registry-side tests avoid disk I/O. Callers depend on the narrowest of `Reader`, `Writer`, or `Store` that satisfies their need.

### Content-addressed storage

The hash assigned at `Put` is `hex(sha256(SourceBytes))`. The snapshot and diagnose bytes do not participate in the hash; they are derived data for the same source. Re-uploading the same source with a different snapshot does not produce a new hash: the existing bundle's hash is returned and the metadata is left unchanged. The Registry is responsible for ensuring CI uploads coherent `(source, snapshot)` pairs; the Store does not validate the derivation relationship.

This means the same operator can upload the same CSV twice and get the same hash both times. Idempotent. The tag system is the mutable layer for "the latest production rules" semantics.

### Filesystem layout

```
<root>/
├── objects/
│   └── <hash[0:2]>/
│       └── <hash>/
│           ├── source         (raw bytes, no extension; Content-Type lives in metadata)
│           ├── snapshot       (raw bytes, optional)
│           ├── diagnose       (raw JSON bytes, optional)
│           └── metadata.json  (Metadata struct)
├── metadata.db                (SQLite)
└── version.txt                (single integer; schema version for migrations)
```

The two-character hash prefix shards the object tree so a single directory does not hold a million entries; Git's object store uses the same pattern. The shard width (2 hex characters → 256 first-level directories) is enough for the volumes a single-region Registry will see.

`metadata.json` on disk duplicates what SQLite indexes. The reason: if SQLite is lost or corrupted, the disk is rebuildable by walking the object tree. SQLite is an index, not the truth.

### SQLite schema

```sql
CREATE TABLE artifacts (
    hash               TEXT PRIMARY KEY,
    content_type       TEXT NOT NULL,
    state              TEXT NOT NULL,         -- staged | active | deprecated
    created_at         INTEGER NOT NULL,      -- unix epoch milliseconds, UTC
    created_by         TEXT NOT NULL,
    source_commit_sha  TEXT,
    description        TEXT,
    derived_by_version TEXT,                  -- producer of derived members, if any
    has_snapshot       INTEGER NOT NULL,      -- 0 | 1
    has_diagnose       INTEGER NOT NULL,      -- 0 | 1
    deprecated_at      INTEGER,
    deprecated_reason  TEXT
);

CREATE INDEX idx_artifacts_state_created ON artifacts(state, created_at DESC);

CREATE TABLE tags (
    tag         TEXT NOT NULL,
    hash        TEXT NOT NULL,
    assigned_at INTEGER NOT NULL,
    PRIMARY KEY (tag, assigned_at)
);

CREATE INDEX idx_tags_current ON tags(tag, assigned_at DESC);
```

`tags` is append-only. `ResolveTag` reads the highest `assigned_at` row for the tag; the natural index serves the lookup. Tag history (the rows underneath) is recorded so the Registry can answer "what did `production` point at last Tuesday?" without a separate audit table.

Foreign keys are enforced at the application layer: `Tag` checks the hash exists before writing the row. SQLite's `PRAGMA foreign_keys = ON` is set too, but the application check is the contract.

### Concurrency

The Store assumes one OS-level writer. SQLite is configured with `_journal_mode=WAL` and `_busy_timeout=5000` so reader-vs-writer contention does not block; the filesystem writes are atomic via tempfile + rename.

`Put` writes bytes to `<root>/objects/<prefix>/<hash>.tmp` first, fsyncs, then renames into place. If the process crashes mid-write, the next start sweeps `*.tmp` files at boot. Repeated `Put` of an existing hash short-circuits before any filesystem work.

### Lifecycle state transitions

```
                  Put
                   │
                   ▼
              ┌─────────┐
              │ staged  │
              └────┬────┘
                   │ Tag (a tag pointing at the hash transitions it implicitly)
                   ▼
              ┌─────────┐
              │ active  │
              └────┬────┘
                   │ Deprecate (operator soft-delete)
                   ▼
              ┌─────────────┐
              │ deprecated  │  (terminal; retained on disk; purge out of scope)
              └─────────────┘
```

`Tag` is the only Store-level operation that transitions `staged → active`: pointing a tag at a hash promotes the underlying artifact. This keeps the substrate's surface narrow and avoids two parallel triggers for the same transition. Non-tag-driven activation (e.g., the registry coordinator promoting a hash to champion without first assigning a tag) is the registry layer's concern; the registry can call `Tag` with an internally-managed tag name or model its own ref alongside the Store.

`Deprecate` from any state other than `deprecated` is allowed; deprecating a `staged` artifact (one that was uploaded but never used) is the same operation as deprecating an `active` one — both move forward to `deprecated`. The Store does not enforce that the Registry has unreferenced the artifact first; that is the Registry's job.

Deprecation is terminal for v1. The compliance-retention rationale (deprecated artifacts must remain inspectable for an audit window) is incompatible with re-activation: an artifact restored to `active` after operator-driven deprecation would conflict with the audit-log claim that it was withdrawn. Recovery from an accidental deprecation is a fresh `Put` — same source bytes return the same hash and create a new `staged` lifecycle row — at the cost of a new audit identity for the restoration event.

### What this package does NOT do

- No HTTP layer. Callers are in-process Go code.
- No env / champion / challenger semantics.
- No bre-go imports. No CSV parsing. No snapshot parsing.
- No format validation (declared Content-Type is trusted).
- No retention policy / purge of deprecated artifacts.
- No cross-region replication. Single-root, single-process.
- No backup / restore tooling beyond "copy the root directory" (out of scope here).

Each of these has a dedicated home in the architecture; none of them belongs in the substrate.

## Consequences

### Closed

- The typed Go API surface for the Store is locked: the `Reader` / `Writer` / `Store` split, the `MemberKind` enumeration of bundle members, the error sentinels. Adding a new bundle member is a one-line constant addition; adding a new operation is a new ADR.
- The artifact bundle shape (a required `MemberSource` plus zero or more derived members) is locked. The current derived members are `MemberSnapshot` and `MemberDiagnose`.
- The filesystem layout is locked. Layout changes require a `version.txt` bump and a migration path.
- The lifecycle state machine (`staged → active → deprecated`, with `Tag` as the sole substrate-level activator and deprecation terminal) is locked.
- The single-writer, single-root, single-region posture is locked for v1.
- The dual storage of metadata (disk JSON + SQLite columns) is locked, with disk as the truth and SQLite as the index.

### Not closed

- **HTTP surface** — exposing the Store to operators over HTTP is the service shell's concern, not this substrate's.
- **In-memory adapter** — `internal/store/memstore` is acknowledged here as the test substrate, but its surface is the same `Store` interface; no separate ADR is needed.
- **Postgres / S3 backings** — recognized migration paths; the typed interface is the abstraction that keeps them swappable.
- **Backup / restore tooling** — operators copy the root for v1; tooling is out of scope here.
- **Deprecated-artifact purge** — the retention policy and purge mechanism are a separate concern.
- **Snapshot derivation tracking** — the Store accepts `(source, snapshot)` as opaque pairs and trusts the uploader to keep them coherent. A derivation manifest, if operators ever need the Store to verify snapshots against their declared source, is out of scope here.
- **Tag namespaces** — v1 tags are a flat string namespace. Per-env namespaces (`prod/v4`, `staging/v4`) are out of scope here.
- **Tag history exposure through the typed interface** — the SQLite `tags` table is append-only and records every re-pointing event, but the `Reader` interface surfaces only current heads via `ListTags`. Operator audit queries that need historical tag-to-hash bindings hit a separate read model, not the `Store` port.
- **Scaling beyond ~10 000 artifacts** — the v1 schema's `List` bar holds for corpora at that order. Higher cardinality would require a covering or secondary index on the all-states path; that schema change is out of scope here.

### Performance impact

The Store's per-operation budgets, pre-registered before measurement and to be filled in under `scientific/v0.0.1/REPORT.md` per the markup-svc/ADR-0012 protocol (absolute bars; bars do not move post-commit). The derivations below assume NVMe SSD storage with `_journal_mode=WAL` and `_synchronous=FULL` (the durable default — every commit `fsync`s the WAL frame); slower storage tiers (gp2 EBS, spinning disk) are called out where they materially change the picture.

| Operation | Bar | Derivation |
|---|---|---|
| `Put` small artifact (~10 KB), source only | < 15 ms | SHA-256 of 10 KB (~25 µs) + payload `fsync` (~3 ms on NVMe SSD, ~10 ms on gp2 EBS) + metadata.json `fsync` (~3 ms) + SQLite INSERT + WAL `COMMIT` `fsync` (~1–3 ms). Expected ~4–6 ms on SSD source-only; ~10–12 ms when source + snapshot + diagnose all uploaded (3 × payload fsync + WAL commit). Bar widened from the gut-feel 5 ms once the WAL commit cost was accounted for; a 5 ms bar would have been unmet on any durable configuration. Operators who prefer the tighter bar can run `_synchronous=NORMAL` (WAL frame fsync skipped per commit, durability degraded to "no data loss on app crash" rather than "no data loss on power loss"); the substrate ships `FULL` and lets operators tune it explicitly. |
| `Put` large artifact (~2 MB) | < 200 ms | Above + SHA-256 of 2 MB (~5 ms) + `fsync` of 2 MB payload (~10–30 ms on NVMe SSD; ~50–100 ms on gp2 EBS) + WAL `COMMIT` `fsync` (~1–3 ms). Expected ~25 ms on NVMe SSD; ~100 ms (pessimistic ~108 ms) on gp2 EBS. The < 200 ms bar leaves ~85–90% of the bar consumed at the pessimistic EBS estimate; CI runners on gp3 or NVMe land well under 100 ms. |
| `GetBundle` (metadata only) | < 5 ms | SQLite SELECT against the artifacts row (~1 ms warm-cache; 1–5 ms cold) + JSON decode of metadata struct (microseconds). No file reads — body bytes are not loaded by `GetBundle`. Expected ~1–2 ms warm; ~3–5 ms cold. Concurrent-write tail: when a `Tag` or `Deprecate` transaction is mid-flight on the single-writer connection pool, a `GetBundle` arriving in the same window queues for the WAL commit `fsync` (~1–3 ms). The < 5 ms bar holds for warm-cache reads in the absence of concurrent writes; under contention, add the WAL commit cost. |
| `GetMember(MemberSource)`, source ≤ ~100 KB warm cache | < 5 ms | SQLite SELECT for `content_type` (~1 ms warm) + single `open + read + close` of the source file. The SELECT can be avoided by reading `content_type` from `metadata.json` on disk; that optimization is open to the implementation. Expected ~1–2 ms. |
| `GetMember(MemberSource)`, multi-MB cold storage | < 50 ms | Above + file read dominated by storage tier — 10–30 ms on NVMe cold cache, 10–50 ms on gp2 EBS. The memstore substrate clones the full source slice per call (isolation invariant); callers on the deploy fast-path at multi-MB scale should hold the returned slice and reuse it, not call `GetMember` repeatedly in a loop. |
| `GetMember(MemberSnapshot \| MemberDiagnose)` | < 5 ms | SQLite SELECT (~1 ms warm) to verify presence + single file read. Expected ~1–2 ms. |
| `List` 1000 artifacts (state-filtered) | < 50 ms | Indexed scan on `idx_artifacts_state_created` (~5–10 ms) + summary marshalling. Expected ~15–20 ms. |
| `List` 1000 artifacts (all states) | < 50 ms | Full table scan (no compound index covers the all-states case) — ~5–15 ms in practice at 1000 rows + marshalling. Expected ~10–20 ms; same bar holds for corpora ≤ ~10 000 rows. At larger corpora a covering or secondary index is needed to stay within the bar — out of scope for the v1 schema and called out as a future schema concern in the Not-closed section. |
| `Tag` | < 10 ms | SQLite SELECT to verify hash exists (~1 ms warm) + single-row INSERT into the tags table + state transition UPDATE on artifacts (~1 ms) + WAL `COMMIT` `fsync` (~1–3 ms). Expected ~3–5 ms. |
| `ResolveTag` | < 5 ms | SQLite indexed point lookup (~1 ms warm; up to 5 ms cold). Expected ~1 ms warm. |
| `Deprecate` | < 10 ms | SQLite UPDATE setting state + deprecated_at + deprecated_reason + WAL `COMMIT` `fsync` (~1–3 ms). Expected ~2–4 ms. |

Per-artifact storage overhead on disk: SHA-256 hash + up to four files. The metadata + diagnose + snapshot files are typically small compared to the source CSV; at 1000 versions of a 100k-rule CSV (~2 MB each) the Store holds roughly 2.5 GB on disk including overhead.

The scientific harness benchmarks call the `fsstore` implementation directly (not through the registry coordinator), so the bars above reflect Store-only cost. The registry coordinator's OTel + jsonlog + Prometheus wrapping is accounted for under `BenchmarkObservabilityOverhead` in the registry-package harness per the model-registry roadmap (< 100 µs per operation), not under the Store bars here. The Store is plain Go without OTel instrumentation; the substrate's interface dispatch cost is ~3–5 ns per call on amd64 (itab lookup, no allocation) — at every bar in this table that is below the measurement floor.
