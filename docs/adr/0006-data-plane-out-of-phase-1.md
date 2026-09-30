# 0006. Data plane moves out of Phase 1 docs

Status: Accepted · Date: 2026-09-30 · Spec: Appendix A · Overview §11

## Context
The data plane (workload-to-workload messaging, hub, object store) is Phase 2, but it takes a large
share of `SPEC.md` and the system overview, which every agent session reads.

## Decision
Move SPEC Appendix A and overview §11 to `docs/proposals/data-plane.md`, leaving a one-line pointer.
Bring them back as spec sections when Phase 1 passes §17.8.

## Consequences
Less context per agent session, so lower usage. The design work is kept, not lost.
