# 0007. Lighter SDLC loop for already-specified work

Status: Accepted · Date: 2026-09-30 · Process: `docs/process.md` §3

## Context
The nine-stage loop has four human gates. Most Phase 1 slices implement behavior `SPEC.md` already
specifies, so a separate spec stage adds work without adding decisions. The maintainer works alone on
a fixed Claude subscription.

## Decision
- If a slice only implements existing § bullets, stages 1–2 collapse into the issue: it lists the
  § bullets and out-of-scope items. No spec PR.
- Human gates: **plan approval** and **PR review/merge**. Tests are reviewed as part of the PR.
- A spec PR is still required when behavior is new, changed, or unspecified.
- One Claude Code session per PR; the issue is the hand-off.

## Consequences
Faster slices and fewer tokens. The spec stays the source of truth because unspecified behavior still
needs a spec change first.
