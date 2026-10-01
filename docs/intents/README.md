---
artifact_id: IEO-INTENTS-001
title: Test infrastructure and example apps for IEO
version: 0.1.0
status: draft
human_checkpoint: owner reviews each intent before it moves to the Spec stage
---

# IEO intents: test infrastructure and example apps

Stage 1 ("Intent") artifacts per `docs/process.md`. Each file states a problem, an outcome,
acceptance criteria and non-goals. Each also works as a GitHub issue body: drop the frontmatter
and paste. Solutions belong to the Spec and Plan stages.

New intents start from [`_template.md`](_template.md) and take the next free `INT-NNN` ID. The
`status` field moves `draft` → `accepted` → `specced` → `done`, or ends at `dropped`. Only a human
sets `accepted`; the spec PR sets `specced` (`docs/process.md` §3). `issue` links the GitHub issue
that tracks the intent, and `repo` says where the work happens.

## Index

| ID | Intent | Repo | Depends on | Priority |
|---|---|---|---|---|
| [INT-001](INT-001-reference-edge-testbed.md) | Reference edge testbed (laptop + BeagleBoard) | `ieo` → `testbed/` | — | P1 |
| [INT-002](INT-002-fault-and-network-injection.md) | Fault and network injection | `ieo` → `testbed/` | INT-001 | P1 |
| [INT-003](INT-003-device-simulators.md) | Device simulators (MQTT, Modbus, OPC UA, camera) | `ieo-examples` → `simulators/` | — | P2 |
| [INT-004](INT-004-conformance-app-ladder.md) | Conformance app ladder | `ieo` → `tests/conformance/` | — | P1 |
| [INT-005](INT-005-smart-store-on-ieo.md) | Smart Store deployed on IEO | `smart-store` | INT-001, INT-004 | P3 |
| [INT-006](INT-006-condition-monitoring.md) | Condition monitoring (gateway tier) | `ieo-examples` → `apps/` | INT-003 | P2 |
| [INT-007](INT-007-visual-inspection.md) | Visual inspection (edge-server tier) | `ieo-examples` → `apps/` | INT-003 | P3 |
| [INT-008](INT-008-line-historian.md) | Line data historian / OEE (stateful) | `ieo-examples` → `apps/` | INT-003 | P2 |

Suggested order: INT-004 and INT-001 first (they serve the Phase 1 vertical slice and §17.8 directly),
then INT-002, then INT-003 with INT-006 and INT-008, and finally INT-007 and INT-005.

## Repositories

- **`ieo`**: the testbed and conformance apps. They change with `SPEC.md`, so they live with it.
- **`ieo-examples`** (new monorepo): industrial apps and shared simulators, versioned per app
  (tags `<app>/vX.Y.Z` = Margo `metadata.version`), path-filtered CI, multi-arch images.
- **`smart-store`**: a product with its own spec. It publishes a Margo package like any
  third-party vendor.

## Coverage matrix (target)

Which spec areas each intent exercises. A row with no mark is a gap.

| Spec area | INT-001 | INT-002 | INT-004 | INT-005 | INT-006 | INT-007 | INT-008 |
|---|---|---|---|---|---|---|---|
| §5 Package contract, import validation |  |  | ● | ● | ● | ● | ● |
| §5.4 Parameters as env vars |  |  | ● | ● | ● | ● | ● |
| §7 State machines, status aggregation |  | ● | ● |  |  |  |  |
| §8.2 Sync loop, §8.8 outbox | ● | ● |  |  |  |  |  |
| §8.6 Autonomous placement, eligibility, capacity | ● |  | ● | ● | ● | ● |  |
| §8.9 EN command handling, `wait`/timeout |  |  | ● | ● |  |  | ● |
| §9.2 Archive safety invariants |  |  | ● |  |  |  |  |
| §14 Failure model and recovery | ● | ● |  | ● |  |  | ● |
| §17.8 Site integration profile | ● | ● | ● |  |  |  |  |
| Multi-arch / constrained host (proposed) | ● |  | ● |  | ● |  |  |
| Large images on slow links |  | ● | ● |  |  | ● |  |
| Stateful volumes across upgrades |  |  |  | ● |  |  | ● |
| App. A data plane (optional) |  |  |  |  | ● |  | ● |

## Open questions across intents

1. **BeagleBoard model.** If it is the BeagleBoard-X15 it is 32-bit ARM (`linux/arm/v7`, 2 GB
   RAM), so every image the gateway runs needs an armv7 build. A 64-bit board needs only arm64.
2. **Single-box site.** Running LO and EN on the BeagleBoard needs a spec change (LO is
   non-hosting today, §3.1). Until then, Site B's LO runs on the laptop.
3. **Windows host.** Testbed and packaging tasks must run on Windows 11 and in Linux CI, so
   Taskfile or Go commands rather than bash.
