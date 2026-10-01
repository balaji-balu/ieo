# 0008. Rename ERA to EN in code

Status: Accepted · Date: 2026-09-30 · Guideline: G-C4

## Context
`SPEC.md` calls the host agent EN; code calls it ERA (`cmd/era`, `internal/era`, `pkg/era`).

## Decision
Rename to `en` (`cmd/en`, `internal/en`, image `ieo-en`) in the branding PR (0001). Environment
variables change from `ERA_*` to `IEO_EN_*`.

## Consequences
Names in code, logs and docs match the spec; reviewers stop translating.
