# Architecture Decision Records

Each file in this folder captures one architecture decision made on the model-registry codebase, following the standard ADR shape (Status / Context / Decision / Consequences).

New decisions get the next number and a short kebab-case slug:

```
NNNN-short-decision-name.md
```

`scripts/check-adrs.sh` (wired into `make ci-local`) verifies that:

1. Every ADR file is indexed in this README.
2. Every README link points at a file that exists.
3. Every ADR file has a `## Status` line with one of: `Proposed`, `Accepted`, `Superseded by ADR-NNNN`, `Deprecated`.
4. Every ADR file has the four standard sections: `## Status`, `## Context`, `## Decision`, `## Consequences`.

## Index

| # | Title | Status |
|---|---|---|
| [0001](0001-model-registry-architecture.md) | Model Registry — control plane for versioned pricing rule artifacts | ✅ Accepted |
| [0002](0002-versioned-config-store-substrate.md) | Versioned Config Store — typed Go substrate | ✅ Accepted |
| [0003](0003-service-shell-and-observability-bootstrap.md) | Service shell and observability bootstrap | ✅ Accepted |
| [0004](0004-read-only-operator-endpoints.md) | Read-only operator endpoints | ✅ Accepted |
| [0005](0005-champion-lifecycle-and-deployer.md) | Champion lifecycle and markup-svc deployer | ✅ Accepted |
| [0006](0006-promotion-gates.md) | Promotion gates — Diagnose verdict short-circuits /promote with 422 | ✅ Accepted |
| [0007](0007-post-promote-canary.md) | Post-promote canary observation + auto-rollback | ✅ Accepted |
| [0008](0008-write-rate-limit.md) | Per-env rate limit on /promote + /rollback | ✅ Accepted |
