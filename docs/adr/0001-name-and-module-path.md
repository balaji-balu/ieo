# 0001. Project name `ieo` and module path

Status: Accepted · Date: 2026-09-30

## Context
The project appears under five names: Go module `margo-hello-world`, images and charts `edgeorch-*`,
installer and docs `edge-orch`, README "Intelligent Edge Orch", and IEO in the spec. The README logo
is labelled "Margo Logo", but Margo is the standard IEO implements, not its brand.

## Decision
- Name: **Intelligent Edge Orchestrator**, short name **`ieo`** everywhere a short name is needed.
- Repository and module: `github.com/balaji-balu/ieo`, by renaming the existing repository
  (history, PRs and issues are kept; GitHub redirects the old URL). Moving to an organization later
  is a repo transfer plus a module path change.
- Binaries: `ieo-co`, `ieo-lo`, `ieo-en`, `edgectl`. Images: `ghcr.io/<owner>/ieo-co|ieo-lo|ieo-en`.
  Helm charts: `ieo-co`, `ieo-lo`.
- "Margo" is used only when referring to the specification ("Margo-conformant").

## Consequences
One rename PR touches every import path; it must land before feature slices start, or every open
branch conflicts. After it, the names in code, docs, images and charts match `SPEC.md`.

## Options considered
- Keep `margo-hello-world`: misleading and suggests an official Margo project.
- A new empty repository: loses history and leaves an old repo to archive.
- `github.com/ieo/ieo`: short, but the `ieo` organization may not be available.
