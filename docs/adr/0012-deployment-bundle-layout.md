# 0012. Deployment bundle layout and deterministic encoding

Status: Proposed · Date: 2026-10-01 · Spec: §4.1.6, §8.1.3, §11.1

## Context
The State Manifest names a bundle: a gzip tar of every deployment YAML of the site (§4.1.6). The
pinned Margo OpenAPI says only that the archive's root holds one YAML file per deployment, exactly
the set the manifest references, and that its digest covers the exact bytes. It names neither the
files nor how the archive is built. The CO writes the bundle and the LO (slice C) reads it, so the
layout must be decided once (G-A2). The manifest names the bundle's digest, so building the bundle
twice from the same deployments must give the same bytes, or identical manifest content would get a
different ETag (§17.2).

## Decision
- One regular file per deployment at the archive root, named `<deploymentId>.yaml` (lowercase UUID),
  holding the deployment's exact YAML bytes. No directories, links or other entries.
- Entries are ordered by deployment ID, ascending, as in the manifest.
- Each tar header is USTAR with mode `0644`, uid and gid 0, empty user and group names, and
  modification time 0 (the Unix epoch).
- The gzip stream has no file name, no comment, modification time 0, and default compression.
- `internal/contract` is the only package that encodes or decodes the bundle.
- The CO builds the bundle when a site's manifest changes and stores its bytes under their digest; it
  never rebuilds a stored bundle.

## Consequences
- The LO can map each entry to its deployment by name and still checks every file against the
  digest in the manifest (§15.3); names are for diagnosis, not trust.
- A different Go release may compress differently. That changes only bundles built after an
  upgrade; stored bundles keep their bytes, so a published digest never changes.

## Options considered
- Name entries `<deploymentId>-<digest>.yaml` — redundant: the manifest already pairs ID and digest.
- Let the archive keep real file times and owners — the bytes, and so the bundle digest, would
  change on every rebuild.
