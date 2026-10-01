# Architecture Decision Records

Decisions on anything `SPEC.md` leaves open or calls "implementation-defined". Order of authority:
Margo → `SPEC.md` → these ADRs → code. Agents follow an **Accepted** ADR without re-asking; to change
one, write a new ADR that supersedes it.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-name-and-module-path.md) | Project name `ieo` and module path | Accepted |
| [0002](0002-strangler-rebuild.md) | Rebuild the Phase 1 core from `SPEC.md` (strangler) | Accepted |
| [0003](0003-en-runs-compose-via-cli.md) | EN runs Compose through the CLI behind an interface | Accepted |
| [0004](0004-co-persistence-ent.md) | CO keeps ent + Atlas on Postgres | Accepted |
| [0005](0005-interim-auth.md) | Interim token auth until mTLS | Accepted |
| [0006](0006-data-plane-out-of-phase-1.md) | Data plane moves out of Phase 1 docs | Accepted |
| [0007](0007-lighter-loop-for-specified-work.md) | Lighter SDLC loop for already-specified work | Accepted |
| [0008](0008-rename-era-to-en.md) | Rename ERA to EN in code | Accepted |
| [0009](0009-laptop-test-environment.md) | Laptop test environment (Windows 11, simulated hosts) | Accepted |

Older design notes in `docs/adr/era/` are superseded by 0002 and 0003 and kept for history.

## Template

```markdown
# NNNN. <Decision in a few words>

Status: Proposed | Accepted | Superseded by NNNN · Date: YYYY-MM-DD · Spec: §x.y

## Context
What forces the decision. Cite SPEC sections.

## Decision
What we do, stated as rules an agent can follow.

## Consequences
What gets easier, what gets harder, what to watch.

## Options considered
- Option — why not.
```
