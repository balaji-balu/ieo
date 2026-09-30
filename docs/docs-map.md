# Documentation Map

How the process documents relate. GitHub renders the diagrams below.

## Mind map: what each document is for

```mermaid
mindmap
  root((AI-native SDLC))
    Principles
      constitution.md
        8 non-negotiable articles
        Amendment rule
    Intent and contract
      SPEC.md
        MUST / SHOULD rules
        §4 domain terms
        §15 security areas
        §17 test matrix
        §18 checklist
        Appendix B slice order
      docs/system-overview.md
        How it works and why
      docs/adr/
        Implementation-defined choices
    Process
      docs/process.md
        Sources of truth and authority
        The loop: 9 stages
        Roles and guardrails
        Definition of done
        Metrics
    Execution rules
      CLAUDE.md
        Agent working rules
        Repo map and commands
        Known baseline issues
      docs/coding-guidelines.md
        A Module design
        B Errors
        C Comments and names
        D Strategic programming
        E Go conventions
        F Tests
      REVIEW.md
        Review order
        Severity 🔴 🟡 🟣
        Red flag → rule ID
```

## Link graph: who references whom

```mermaid
flowchart LR
  const["constitution.md<br/><i>principles</i>"]
  margo[/"Margo specification<br/>(external)"/]
  spec["SPEC.md<br/><i>implementation contract</i>"]
  overview["docs/system-overview.md<br/><i>design and reasons</i>"]
  adr["docs/adr/<br/><i>decisions</i>"]
  process["docs/process.md<br/><i>the loop</i>"]
  claude["CLAUDE.md<br/><i>agent rules</i>"]
  guide["docs/coding-guidelines.md<br/><i>G-A1…G-F5</i>"]
  review["REVIEW.md<br/><i>review checklist</i>"]

  const -- "governs" --> process
  const -- "governs" --> spec
  const -- "governs" --> guide
  const -- "governs" --> review
  const -- "governs" --> claude
  margo -- "authoritative over [Margo] rules" --> spec
  spec <-- "explains / implements" --> overview
  adr -- "fills gaps in" --> spec

  process -- "sources of truth, §17/§18, App. B" --> spec
  process -- "lists" --> overview
  process -- "lists" --> adr
  process -- "enabler" --> claude
  process -- "enabler" --> guide
  process -- "enabler" --> review

  claude -- "follow the loop" --> process
  claude -- "spec first, cite §" --> spec
  claude -- "cite rule IDs" --> guide
  claude -- "run /code-review" --> review
  claude -- "decisions" --> adr

  guide -- "rules per stage" --> process
  guide -- "§4 terms, §13.1 logs, §17 tests" --> spec

  review -- "stage 7" --> process
  review -- "spec fidelity, §15, §17" --> spec
  review -- "red flags by ID" --> guide
```

## How they are used in the loop

| Loop stage (`docs/process.md` §3) | Documents read | Documents written |
| --- | --- | --- |
| 1 Intent · 2 Spec | `SPEC.md`, `docs/system-overview.md`, `docs/adr/` | `SPEC.md`, ADR |
| 3 Plan | `CLAUDE.md`, `SPEC.md`, `docs/coding-guidelines.md` (A, G-A7) | Plan |
| 4 Tests | `SPEC.md` §17, `docs/coding-guidelines.md` (F) | Tests |
| 5 Implement | `CLAUDE.md`, `docs/coding-guidelines.md` (B, C, E) | Code |
| 6 Verify | `CLAUDE.md` (commands) | — |
| 7 Review | `REVIEW.md`, `docs/coding-guidelines.md`, `SPEC.md` | Review findings |
| 8 Merge | `docs/process.md` §5, `SPEC.md` §18 | §18 checklist |
| 9 Learn | Review history | `CLAUDE.md`, `docs/coding-guidelines.md` |

## Documents

- [constitution.md](../constitution.md)
- [SPEC.md](../SPEC.md)
- [docs/system-overview.md](system-overview.md)
- [docs/adr/](adr/)
- [docs/process.md](process.md)
- [CLAUDE.md](../CLAUDE.md)
- [docs/coding-guidelines.md](coding-guidelines.md)
- [REVIEW.md](../REVIEW.md)
