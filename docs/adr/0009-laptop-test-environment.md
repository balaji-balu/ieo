# 0009. Laptop test environment (Windows 11, simulated hosts)

Status: Accepted · Date: 2026-09-30 · Spec: §17.8

## Context
All Phase 1 testing runs on the maintainer's Windows 11 laptop: 1 CO, 1 LO and 2 ENs (§17.8).
Two ENs sharing one Docker engine would see each other's containers, so inventories and removals
would mix. Bash scripts and Makefiles don't run natively on Windows.

## Decision
- Runtime: Docker Desktop (WSL 2 backend). Podman Desktop also works but is not the default.
- One Compose file, `deploy/dev/compose.yaml`, starts NATS, Postgres, a local OCI registry
  (`registry:2`), CO, LO and **two simulated hosts**. Each host is a privileged `docker:dind`
  container with its own Docker engine, running one EN. Hosts are isolated like real machines.
- Everything is started with `docker compose` and `go` commands that behave the same in PowerShell
  and bash. No shell scripts; helpers are Go programs under `tools/` run with `go run`.
- Unit tests (`go test ./...`) run natively on Windows. Code must not assume `/` paths
  (use `filepath`) or Unix-only syscalls outside the EN runtime package.
- CI (Linux) runs the same unit tests plus the §17.8 golden path using the same Compose file.

## Consequences
The laptop reproduces a two-host site. dind needs about 1–2 GB RAM per host. Network-cut tests use
`docker network disconnect`, which works on Docker Desktop.

## Options considered
- ENs as plain processes on the laptop: they share one engine, so §17.8 isolation tests can't run.
- WSL-only development: works, but forces a Linux shell for every command.
