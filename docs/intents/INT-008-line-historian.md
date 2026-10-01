---
artifact_id: INT-008
issue:
title: Line data historian and OEE
repo: ieo-examples (apps/line-historian/)
status: draft
priority: P2
depends_on: [INT-003]
affected_spec: ["§5.4", "§8.1", "§8.9", "§14.2", "App. A"]
human_checkpoint: owner agrees it is worth doing
---

# INT-008: Line data historian and OEE

## Problem
Most real edge apps keep data: historians, local databases, buffers. IEO's examples are
stateless, so nothing shows whether data survives a version update, a host restart or a
redeployment, or what the operator must do to keep it.

## Outcome
A historian app that collects machine state and counts from the INT-003 PLC and OPC UA simulators
into a local time-series database, computes OEE (availability, performance, quality) per shift,
and shows it on a small dashboard.

## Acceptance criteria
- Data persists across: an app version update, an EN restart and a host reboot.
- The data's behavior on deployment delete is documented and deliberate (kept or removed).
- Collection continues through a central-link outage with no gaps in stored data.
- A schema change between versions is handled by the app on start, not by manual steps.
- OEE figures match values computed by hand from the simulator's known output.

## Success metrics
- No data loss across five consecutive version updates on the testbed.

## Non-goals
- Long-term retention or central aggregation (a later data-plane use case, `docs/proposals/data-plane.md`).
- High availability of the database.

## Open questions
- Does IEO need a spec rule on volume lifecycle (kept or removed on delete)? This intent may
  surface a gap in §8.9 and §9.1.
