# 0015. LO store: `hosts` and `actual` buckets (layout version 2)

Status: Proposed · Date: 2026-10-08 · Spec: §4.1.11, §12, §7.3, §8.5 · Refines: 0014

## Context
Roadmap slice D (balaji-balu/ieo#58) has the LO learn hosts and their actual state from EN
inventories and status events. It then plans Apply/Remove from them (§8.5). §4.1.11 makes `hosts`
(capabilities, labels, `last_heartbeat_at`) and `actual` (each host's last reported deployments)
durable. ADR 0014 laid out version 1 of `lo.db` with `meta`, `sync` and `desired`, and says a later
slice adds its own buckets. It also says a store whose bucket is missing is damaged and is refused,
so new buckets need a layout version and a migration.

## Decision
- Layout version 2 adds two buckets beside version 1's; the existing buckets keep their encoding:

  | Bucket | Key | Value |
  | --- | --- | --- |
  | `hosts` | host ID (§4.2) | JSON `{"capabilities", "labels", "lastHeartbeatAt"}`: `capabilities` is the §11.2 `Capabilities.capabilities` object or `null` if none has been received; `labels` an object, `{}` if none; `lastHeartbeatAt` RFC 3339 or `null` |
  | `actual` | host ID (§4.2) | JSON `{"reportedAt", "deployments"}`: `reportedAt` RFC 3339; `deployments` an array of §11.2 `Inventory` deployment entries (`deploymentId`, `digest`, `components` with `name`, `state` and optional `error`) |

- Host IDs are the `<h>` of the subject a message arrived on; a payload `hostId` that differs is
  dropped before it reaches the store (SPEC §11.2).
- A host enters `hosts` with its first inventory, heartbeat or capabilities message, and leaves it
  only when it is decommissioned (§7.3, roadmap I). `actual[h]` is replaced whole by each
  inventory from `h`.
- A status event changes the one component it names (§7.5) when its digest is the digest
  `actual[h]` holds for that deployment. An event at another digest, or for a deployment `actual[h]`
  does not hold, replaces that deployment's entry with the event's digest and that one component.
  Components with no report at the new digest are then absent, which the planner and status
  mapping read as `pending` (SPEC §8.7). States from the old digest are never carried over.
- `OpenBolt` migrates version 1 to 2 in one write transaction: it creates the empty `hosts` and
  `actual` buckets and sets `schema` to 2. A new file is created at version 2. A file at any other
  version is refused at open, as ADR 0014 says.
- `Load` fails on a record it cannot decode (ADR 0014), and never returns a partial `hosts` or
  `actual`.
- Liveness is not stored: it is derived from `lastHeartbeatAt` (§4.1.11). Until slice I, the interim
  `Online` rule in §7.3 is in memory only, so after an LO restart every host is `Unknown` until its
  next inventory, which the LO asks for at start (§16.2).

## Consequences
- An LO that restarts knows its hosts and their last actual state, even before any EN answers.
  It sends no command until a host has published inventory again (§7.3 interim rule), so stale
  actual state never drives a command.
- An LO from slice C cannot open a version 2 file. Rolling back to it means deleting `lo.db` and
  re-syncing (§12).
- Each inventory rewrites one record per host, which is small and infrequent (`en.inventory.interval`).

## Options considered
- Keep `hosts` and `actual` in memory until slice L, with an `[interim]` note in §4.1.11: less code
  in D, but it weakens a MUST and leaves the store for L to add under time pressure.
- One bucket per host, holding its host record and nested deployments: no JSON arrays, but more
  buckets to keep consistent when a host is replaced or decommissioned.
- Add the buckets without raising `schema`, and create them lazily: a missing bucket could then mean
  either an old file or damage, which ADR 0014 keeps distinct.
