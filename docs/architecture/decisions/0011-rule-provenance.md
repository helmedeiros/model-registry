# 11. Rule-level provenance metadata

## Status

Accepted — `store.Metadata` carries an optional `Rules []RuleProvenance` slice (rule_id, author, source_commit_sha, pr_url, description, last_modified). The upload path accepts the slice through the existing `metadata` multipart part. The artifact read surface (`GET /artifact/{hash}`, `GET /artifacts`) surfaces it. A new `GET /artifact/{from}/diff/{to}` returns the added / removed / modified rules between two bundles, keyed by `rule_id`. mrctl gains a `diff` subcommand. The registry remains format-agnostic: it does NOT parse the source bytes to validate that `rule_id`s match the rules actually present in the CSV / JSON.

## Context

ADRs 0001-0010 stored the bundle as opaque bytes plus operator-facing bundle metadata (`created_by`, `source_commit_sha`, `description`). Per-rule attribution did not exist. After an auto-rollback the operator could see "rolled back from hash A to B" but not "rule `premium_uplift` was the change that broke it, introduced by `alice` in PR #423". For incident response and compliance review, that gap was the bottleneck.

The authoring pipeline (markup-svc rules repo) already tracks per-rule git blame: each CSV row has a commit in its history. The registry has no way to ingest that without either (a) parsing the CSV and walking git history at upload time, or (b) accepting the provenance as caller-supplied metadata. (a) couples the registry to a rule-file format and a VCS; (b) keeps the registry format-agnostic but moves the responsibility upstream. This ADR picks (b).

## Decision

### Schema

```go
type RuleProvenance struct {
    RuleID          string
    Author          string
    SourceCommitSHA string
    PRURL           string
    Description     string
    LastModified    time.Time
}

type Metadata struct {
    // ...existing fields
    Rules []RuleProvenance
}
```

The wire layer mirrors this in `RuleProvenanceJSON` with `omitempty` on every field except `rule_id`. The slice itself is omitted from `ArtifactMetaJSON` when empty so older bundles (uploaded before this ADR) marshal exactly as they used to.

### Upload

`POST /upload`'s `metadata` part is now decoded into `UploadMetadata` (the wire type) and mapped to `store.Metadata` via `fromUploadMetadata`. The wire-to-domain boundary is enforced rather than implicit — a future field added only to one side fails the build instead of silently leaking. The uploader sets the slice; the registry stores it verbatim. No validation that `RuleID`s match the source bytes; that would re-introduce the format coupling this ADR specifically avoided.

The fsstore substrate gained a `rules_json TEXT` column (schema version bumped from 1 to 2; the migration is an idempotent `ALTER TABLE … ADD COLUMN` that swallows the duplicate-column error). `Put` writes the marshalled slice into the new column alongside the existing JSON sidecar; `GetBundle` and `List` decode it back. A storetest conformance case (`PutRoundTripsRuleProvenanceThroughGetBundle` + `ListSurfacesRuleProvenance`) covers both backings — a regression that drops Rules on either path fails the suite.

### Read

`toMetaJSON` (extracted from the previously-duplicated inline mappings in `artifacts.go` and `artifact.go`) is the single conversion point from `store.Metadata` to `ArtifactMetaJSON`. Both `GET /artifacts` (list) and `GET /artifact/{hash}` (bundle) call it; both surface the rules block when populated.

### Diff

`GET /artifact/{from}/diff/{to}` returns:

```json
{
  "from": "aaa…",
  "to":   "bbb…",
  "added":    [ { "rule_id": "loyalty_discount", "author": "carol", ... } ],
  "removed":  [ { "rule_id": "weekend_uplift",   "author": "bob",   ... } ],
  "modified": [
    {
      "from": { "rule_id": "premium_uplift", "source_commit_sha": "abc1234", ... },
      "to":   { "rule_id": "premium_uplift", "source_commit_sha": "def5678", ... }
    }
  ]
}
```

A rule is `modified` when `RuleID` matches between the two bundles and any of the other provenance fields differ. `added` / `removed` are sorted by `rule_id` for deterministic output. The handler returns `404 from_not_found` / `404 to_not_found` if either hash is unknown.

When either bundle has no `Rules` slice (older artifact uploaded before this ADR), every rule on the other side is reported as added or removed. That degrades cleanly: the diff still answers "what's different" structurally, but the human story is missing. The operator's signal is the empty `Rules` block on the older side.

### mrctl

`mrctl diff <from-hash> <to-hash> [--json]` renders:

```
from: aaa1234…
to:   bbb5678…
added:
  loyalty_discount               999abcd0 by carol
removed:
  weekend_uplift                 def56789 by bob
modified:
  premium_uplift
    from: abc12340 by alice
    to:   def56789 by alice
```

## Consequences

### Positive

- Auto-rollback incident response: the operator now reads `mrctl diff <broken> <prev>` and sees which rule changed, who authored it, and the PR URL. That's the human story that closes the gap from ADR-0007's canary auto-rollback.
- Compliance: per-rule attribution lands in the audit-adjacent surface (the artifact bundle) without needing a separate audit-of-rules ledger.
- Format-agnostic: the registry's substrate continues to treat the source bytes as opaque. The authoring pipeline (which already has the data) is the only thing that needs to populate the slice.
- Forward-compatible: bundles uploaded before this ADR have no `Rules` block; reads marshal cleanly without it. No migration script.

### Negative

- Trust boundary: the registry takes the uploader's word that `RuleID`s match the source bytes. A mis-attributed rule (uploader sends `premium_uplift` provenance with a CSV that no longer contains that rule) silently lands. The diff endpoint reports based on the metadata, not the bytes. Mitigation: the upload path runs Diagnose against the bytes; the same Diagnose can be extended to cross-check provenance, but that work is parked.
- The diff is a metadata diff, not a content diff. Two bundles with identical `Rules` blocks but different source bytes are reported as "no rule changes". This is by design — the substrate does not parse the bytes — but operators reading the output must understand that "no diff" means "no provenance change", not "byte-identical".
- Wire-shape additive: the `Rules` field is `omitempty`, so existing consumers that don't know about it ignore it. A typed client that round-trips `ArtifactBundle` through its own struct will need a `Rules` field added, but the JSON wire stays backward-compatible.

### Deliberately not here

- Source-byte parsing to validate `RuleID`s against the actual rule content. Tracked as a future Diagnose extension.
- Per-rule audit ledger ("who changed what when across all bundles"). The current `audit.Entry` records bundle-level actions; per-rule activity is reconstructible by walking history + diffing but is not a first-class surface.
- Annotation-style git blame for `RuleID` history across all bundles ("walk back to find when `premium_uplift` was introduced"). A useful future endpoint but requires a different data model than per-bundle metadata.

## Alternatives considered

**Parse source bytes at upload time, derive provenance from git** — would make the registry the source of truth. Rejected: couples the registry to CSV/JSON format AND to a VCS, both of which are explicitly out of scope for the substrate (ADR-0001).

**Store provenance in a separate sidecar member** — would keep `Metadata` lean. Rejected: every read of `GET /artifact/{hash}` would need a second member fetch to surface rules in the bundle response. The slice scales linearly: at ~72 bytes per `RuleProvenanceJSON` (six string headers × 16 bytes on amd64) a 100-rule bundle inlines ~7 KB of short-lived heap per read. Acceptable at the registry's expected read QPS, not yet measured under a benchmark — a `BenchmarkGET_Artifact_WithRules` is parked for the harness extension when the authoring pipeline starts populating non-trivial Rules counts.

**Diff endpoint takes diff-by-content (parse source, diff rules)** — would be the "right" semantic answer. Rejected for the same reason as the parse-at-upload alternative: format coupling. The metadata diff is the leanest path to "what did the operator change", and it's honest about the limitation.
