# model-registry

Control plane for versioned pricing rule artifacts.

The Model Registry stores versioned rule sets, tracks champion/challenger state per environment, runs promotion gates, and deploys artifacts to running [markup-svc](https://github.com/helmedeiros/markup-svc) instances via body-based reload. It's the control plane that complements markup-svc's data plane.

## Architecture (two layers)

- **Versioned Config Store** — content-addressed blob store for [bre-go](https://github.com/helmedeiros/bre-go) canonical CSV artifacts. Optionally carries pre-compiled indexed snapshots alongside each CSV (a build artifact for fast cold-start at 100k+ rule sets). Pure storage; treats all artifacts as opaque bytes.
- **Model Registry** — service tracking champion/challenger state per environment, running declarative promotion gates, orchestrating deployment to markup-svc instances, keeping the audit trail, and exposing the operator API.

The Registry does not vendor bre-go parsers. Format awareness lives entirely in bre-go (the shared substrate every consumer depends on); the Registry dispatches via HTTP `Content-Type`.

Requires markup-svc with body-based `POST /admin/reload` support; older markup-svc deployments serve via file-on-disk + reload-trigger and are not managed by the Registry.

See [docs/architecture/decisions/](docs/architecture/decisions/) for design decisions.

## Status

Pre-v0.0.1. Substrate work in progress. See [CHANGELOG](CHANGELOG.md).
