# 0004. CO keeps ent + Atlas on Postgres

Status: Accepted · Date: 2026-09-30 · Spec: §4, §7 (overview)

## Context
The CO needs to store deployments, immutable revisions by digest, and a per-site manifest with a
strictly increasing `manifestVersion`. Today Postgres holds only apps and status; desired state lives
in Git.

## Decision
- Keep ent (generated, never hand-edited) with Atlas versioned migrations.
- Add `Deployment` (ID, site, target, current digest), `DeploymentRevision` (digest, exact YAML
  bytes, immutable) and `SiteManifest` (site, `manifestVersion`, ETag) in the slice that needs them.
- Manifest version increments happen in the same transaction as the deployment change.

## Consequences
No new tooling. Schema changes follow `docs/contributing.md`.

## Options considered
- sqlc: explicit SQL, but a second persistence style in one repo for little gain now.
