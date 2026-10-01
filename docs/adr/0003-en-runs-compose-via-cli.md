# 0003. EN runs Compose through the CLI behind an interface

Status: Accepted · Date: 2026-09-30 · Spec: §16.6, §17.6

## Context
SPEC.md requires EN to run each component as a Compose project on Docker or Podman, but does not
say how.

## Decision
- EN calls `docker compose` or `podman compose` as a subprocess, selected by config.
- All runtime calls go through one interface in the EN compose package
  (for example `ComposeRunner` with `Up`, `Down`, `List`), with `context.Context` on every call.
- §17.6 tests use a fake runner; a real-runtime test runs only in the §17.8 site profile.

## Consequences
Works with Docker Desktop and Podman Desktop on Windows, and with both engines on Linux hosts. The
CLI must be installed on the host (checked at EN start-up). Output parsing is limited to
`compose ls/ps --format json`.

## Options considered
- `compose-go` library + Docker API: more code, Docker-only, harder to support Podman.
