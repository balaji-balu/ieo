# 0014. LO store: a new bbolt store and its file layout

Status: Proposed · Date: 2026-10-07 · Spec: §12, §4.1.11, §14.2 · Refines: 0002 (the "keep" line for `internal/lo/boltstore`)

## Context
§12 gives the LO an "embedded key-value store" and leaves its layout to the implementation.
Roadmap C2 stores the sync state in it (`accepted_manifest_version`, `etag`, `desired`). Later
slices add placements, hosts, actual state and the outbox beside these fields, so the layout must
grow without migrating what is there. That makes it a lasting format, which this ADR records.

ADR 0002 keeps and adapts `internal/lo/boltstore` for its single-writer loop. That package
depends on the old `pkg/model` types, its tests don't compile (`BROKEN_TESTS`), and legacy LO
code imports it. bbolt already runs one write transaction at a time, so the writer goroutine adds
a goroutine to own and stop (G-E5) without adding safety. The single authority of §7.6 belongs to
the LO's event loop, not to its store.

## Decision
- The LO store is `store.Bolt` in `internal/lo/store`, a new store that uses `go.etcd.io/bbolt`
  directly and implements the same `losync.Store` as `store.Memory`. It does not import
  `internal/lo/boltstore`, which is deleted with the legacy code that imports it.
- `OpenBolt(path)` creates the file with mode `0600` and waits at most 1 s for the file lock, so a
  second LO on the same file fails at startup instead of hanging.
- Layout version 1:

  | Bucket | Key | Value |
  | --- | --- | --- |
  | `meta` | `schema` | layout version, 8 bytes big-endian |
  | `sync` | `version` | `accepted_manifest_version`, 8 bytes big-endian; `0` = none accepted |
  | `sync` | `etag` | ETag of the accepted manifest's response; empty = none |
  | `desired` | deployment ID (lowercase UUID string) | JSON `{"digest", "adoptedManifestVersion", "yaml"}`, `yaml` in base64 |

- An empty file gets every bucket and key above, so a missing key or bucket later means damage.
  A file with buckets but no `meta`, or with another `schema`, is refused at open.
- `ReplaceDesired` deletes and recreates `desired` in one transaction; `CommitVersion` writes
  `version` and `etag` in another (§8.2 steps 6 and 8). bbolt syncs to disk on each commit.
- `Load` fails on any key, record or bucket it cannot decode, and on a YAML that does not match its
  digest (§15.3). It never returns an empty state instead (§12, §14.2).
- A later slice adds its own buckets beside these. A change to an existing bucket's encoding
  raises `schema` and comes with a migration in `OpenBolt`, recorded in an ADR that refines this one.

## Consequences
- An LO restart keeps rollback protection, and a damaged file stops the LO from accepting
  manifests rather than silently resetting it. Recovery from damage is an operator action (delete
  the file; the LO re-syncs, §12), covered end to end in roadmap L.
- Each `ReplaceDesired` rewrites every deployment's YAML, even unchanged ones; YAML is small.
- The YAML is about a third larger on disk than raw bytes, for one JSON encoding.
- `internal/lo/boltstore` stays until its legacy importers are deleted.

## Options considered
- Adapt `internal/lo/boltstore` (ADR 0002 as written): patches old code with old types, and keeps a
  writer goroutine bbolt doesn't need.
- A nested bucket per deployment with raw `digest`, `adopted` and `yaml` keys: no base64, but three
  keys per deployment to keep consistent.
- YAML in a `blobs` bucket by digest, with `desired` holding `{digest, adopted}`: no rewrite of
  unchanged YAML, but every replace must clean up blobs no deployment uses.
- Treat a record that does not decode as absent: the store would come back empty or partial and
  reset rollback protection.
