---
artifact_id: INT-002
issue:
title: Fault and network injection
repo: ieo (testbed/)
status: draft
priority: P1
depends_on: [INT-001]
affected_spec: ["§7.3", "§7.4", "§8.2", "§8.8", "§14.1", "§14.2", "§17.8"]
human_checkpoint: owner agrees it is worth doing
---

# INT-002: Fault and network injection

## Problem
IEO's main promise is that a site stays converged while disconnected and catches up in one step
when it reconnects (§1, §14.2). Nothing today produces the failures that promise is about:
long outages, slow or lossy links, metered bandwidth, power loss in the middle of a pull, a full
disk. Recovery behavior is asserted in the spec but not demonstrated.

## Outcome
Named, repeatable fault scenarios that can be applied to any testbed link or host from INT-001, and a
report showing the system's observed behavior against §14.2 for each scenario.

Fault classes, mapped to §14.1:

| Fault | §14.1 class | Example scenario |
|---|---|---|
| CO ↔ LO link down for minutes to hours | Central link | Two deployment changes made while offline; one poll converges |
| High latency, packet loss, low bandwidth | Central link | Sync completes under 1 s RTT and 5% loss |
| LO ↔ EN link down | Site link | EN marked offline after missed heartbeats; reconciled on return |
| Process kill / host power-off mid-operation | Restart | EN killed while pulling an archive; no half-applied deployment |
| Disk full on EN | Execution | Pull fails cleanly, component `failed`, recovers after space is freed |
| Tampered or stale manifest | Integrity | Lower `manifestVersion` rejected and logged |

## Acceptance criteria
- Each scenario can be started by name against a chosen site or host, and undone.
- Each scenario has an expected outcome taken from §14.2 and a pass/fail check.
- A run produces one report: scenario, expected, observed, pass/fail, with relevant logs and
  metrics.
- The §17.8 site-autonomy and outbox scenarios use this mechanism, not ad-hoc steps.
- Scenarios work on the simulated site in CI; the power-off scenario also has a manual bench
  procedure for the BeagleBoard.

## Success metrics
- Every row of the §14.2 table has at least one passing scenario.
- A 24-hour disconnected run (from `docs/architecture/gaps.md`) completes with the site
  converged and the outbox flushed on reconnect.

## Non-goals
- Chaos testing of the CO's database or high availability.
- Security penetration testing beyond the integrity cases above.

## Open questions
- Is a 24-hour run a CI job (nightly) or bench-only?
