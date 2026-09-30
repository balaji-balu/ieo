# Constitution — Intelligent Edge Orchestrator

Status: v1

These are the project's non-negotiable principles. Every other document works out the details;
none of them may contradict this one.

## Articles

1. **The spec is the contract.** Behavior is defined in `SPEC.md`, not in code. A behavior change
   updates the spec in the same change. Where the spec is silent, the answer goes into the spec or an
   ADR, not into code alone.
2. **Margo first.** For `[Margo]` rules, the Margo specification is authoritative. Where this
   project disagrees with Margo, this project has a bug.
3. **Humans own intent; agents own execution.** Humans decide what to build, approve specs, plans and
   tests, make architecture decisions and merge. Agents plan, test, implement and respond to review
   inside the approved scope.
4. **Tests are the executable spec.** No behavior without a test that was seen failing first.
5. **Every change is small, traceable and machine-verified.** One slice per PR, § references
   everywhere, CI green before review.
6. **Complexity is the enemy.** Code follows `docs/coding-guidelines.md`: deep modules, hidden
   information, errors defined out of existence, obvious code.
7. **Safety before autonomy.** The deterministic core decides; intelligent features only advise and
   always have a baseline fallback. Security-sensitive areas (§15) always get human review. No
   secrets anywhere in the repository.
8. **The process learns.** Repeated mistakes become rules in `CLAUDE.md`, the guidelines or lint
   rules.

## Documents under this constitution

| Document | Governs |
| --- | --- |
| `SPEC.md`, `docs/system-overview.md`, `docs/adr/` | What the system does and why |
| `docs/process.md` | How every change moves from intent to merge |
| `docs/coding-guidelines.md` | How code is written |
| `REVIEW.md` | How changes are reviewed |
| `CLAUDE.md` | How agent sessions behave |

`docs/docs-map.md` shows how they link.

## Amendments

The constitution changes only by a PR that a human approves and whose description explains why.
Documents that conflict with the amended version are updated in the same PR.
