# REVIEW.md

Instructions for AI code review (`/code-review`, stage 7 of `docs/process.md`). Human reviewers can
use the same checklist. The AI reviewer reports findings; it never approves or merges.

## What to check, in order

1. **Correctness.** Bugs, races, leaks, broken error paths, violated invariants.
2. **Spec fidelity.** The change does what the cited `SPEC.md` sections say, and nothing the spec
   forbids. Behavior changes without a `SPEC.md` change in the same PR are a finding.
3. **Security.** For §15 areas (TLS, credentials, enrollment, archive extraction, §9.2 safety
   invariants) and any input crossing a trust boundary (§15.1).
4. **Tests.** New behavior has §17 tests named after their bullet (G-F2), testing the public
   interface (G-F1), deterministic (G-F4). No test skipped, disabled or weakened.
5. **Guidelines.** Red flags from `docs/coding-guidelines.md`, cited by rule ID.

## Severity

Start each finding with its severity so authors and agents know what blocks the merge.

| Severity | Use for | Blocks merge |
| --- | --- | --- |
| 🔴 **blocking** | Bugs; security issues; spec violations; missing §17 tests for new behavior; swallowed errors (G-B3); panics in request/sync/reconcile paths (G-B4); goroutine leaks or races (G-E5); missing §13.1 log fields on deployment logs (G-E6); secrets (G-E7); hand-edited generated code (G-E8); information leakage across CO/LO/EN boundaries (G-A2) | Yes |
| 🟡 **nit (optional)** | Other guideline red flags: shallow modules, pass-through methods, vague names, comments that repeat code, inconsistency with the package's style | No |
| 🟣 **pre-existing (not blocking)** | Problems in code the PR didn't change | No |

When unsure between 🔴 and 🟡, pick 🔴 only if you can describe a concrete failure (inputs → wrong
behavior) or cite a MUST in `SPEC.md`.

## Guideline red flags to look for

| Red flag in the diff | Rule |
| --- | --- |
| Interface nearly as complex as the implementation; many thin exported functions | G-A1 |
| Same format/layout/encoding knowledge in two packages or tiers | G-A2 |
| Files or functions split by execution phase that share one data format | G-A3 |
| Function that only forwards its arguments; parameter threaded through layers that don't use it | G-A4 |
| Callers must set options to get the common behavior | G-A5 |
| Business policy inside a generic utility | G-A6 |
| Callers handling "already exists" / "not found" instead of an idempotent operation | G-B1 |
| Retry/backoff logic duplicated across callers | G-B2 |
| `_ = err`, log-and-return-nil, error messages without identifiers | G-B3 |
| Exported identifier without a doc comment; doc comment describing internals | G-C1, G-C3 |
| Comment restating the code; spec logic without a `// SPEC §x.y` reference | G-C2 |
| Names that differ from `SPEC.md` §4 terms, or vague names (`data`, `info`, `mgr`) | G-C4 |
| I/O without `context.Context`; package-level mutable state | G-E2, G-E3 |

## How to write a finding

- One finding per problem, anchored to the line.
- State the defect in one sentence, then a concrete failure scenario or the rule it breaks
  (`SPEC §8.5 MUST …`, `G-B3`).
- Suggest the smallest fix. Don't ask for refactors outside the PR's slice; flag them as 🟣 instead.
- Don't comment on formatting or anything golangci-lint already enforces.

## Out of scope for review

- Design choices already approved in the plan or an ADR (question them in the spec loop instead).
- Requests to widen the PR beyond its Appendix B step.
