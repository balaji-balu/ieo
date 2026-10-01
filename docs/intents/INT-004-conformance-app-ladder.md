---
artifact_id: INT-004
issue:
title: Conformance app ladder
repo: ieo (tests/conformance/)
status: draft
priority: P1
depends_on: []
affected_spec: ["§5", "§7.1", "§7.2", "§8.4", "§8.6", "§8.9", "§9.2", "§10", "§17"]
human_checkpoint: owner agrees it is worth doing
---

# INT-004: Conformance app ladder

## Problem
Testing IEO needs Margo applications. The existing fixtures (`tests/hello.yaml`, `app1–3.yaml`,
`compose1.yaml`) are ad hoc: it is not clear which spec rule each one exercises, and several
rules have no app that exercises them at all. When a test fails, a large example app makes the
cause hard to find.

## Outcome
A ladder of tiny Margo applications, each built to exercise one spec behavior, packaged and
pushed to the local registry by one command, and named after the rule they cover:

| Rung | App exercises | Spec |
|---|---|---|
| 1 | Single component, directed deployment | §8.4, §8.9 |
| 2 | Two components with `wait` and `timeout` | §8.9, §7.2 |
| 3 | Parameters injected as environment variables | §5.4 |
| 4 | Eligibility rules and host labels | §8.6 |
| 5 | Capacity requirements; no eligible host | §8.6 (`IEO-NO-ELIGIBLE-HOST`) |
| 6 | Crash-looping component | §7.1, §14.2 |
| 7 | Update to a new version, then back to the previous one (rollback is not yet specified; see `gaps.md`) | §8.1, §17.2 |
| 8 | Large image on a slow link | §8.9, §14.2 |
| 9 | Multi-arch image placed on ARM and x86 hosts | proposed device-class rule |
| 10 | Invalid packages and archives (must be rejected) | §5.3, §5.2, §9.2 |

## Acceptance criteria
- Each rung is a complete Margo package (Application Description plus Compose archive) that
  passes or deliberately fails CO import validation as intended.
- Each rung's README names the spec section and the §17 bullets it serves.
- One command builds and pushes every rung to the testbed registry.
- Existing ad-hoc fixtures are either mapped to a rung or retired.
- Rung images are small (target under 20 MB) and multi-arch.

## Success metrics
- Every §17 bullet that needs an application has a rung to use.

## Non-goals
- Realistic business functionality (that is INT-005 to INT-008).
- Helm or other deployment types.

## Open questions
- Should rung 10's invalid packages live as committed fixtures or be generated at test time?
