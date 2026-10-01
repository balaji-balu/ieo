---
artifact_id: INT-001
issue:
title: Reference edge testbed
repo: ieo (testbed/)
status: draft
priority: P1
depends_on: []
affected_spec: ["§3.4", "§8.3", "§8.6", "§14", "§17.8", "§18.3"]
human_checkpoint: owner agrees it is worth doing
---

# INT-001: Reference edge testbed

## Problem
CO, LO and EN are tested together on one laptop, all as the same kind of host. Behavior that
depends on more than one site, on constrained or ARM hosts, or on real hardware is unverified.
§17.8 (Site Integration Profile) and §18.3 (validation on the target OS and runtime) have no
standing environment to run in.

## Outcome
A reproducible testbed, started with one command, with two sites that differ the way real edge
sites differ:

- **Site A (simulated, on the laptop):** one LO plus two ENs in containers. One EN is
  "server class" (x86, unrestricted). One is "gateway class" (ARM image under emulation, capped
  at 1 GB RAM and 1 CPU, labelled as a gateway).
- **Site B (real, BeagleBoard):** one EN on the BeagleBoard. Its LO runs on the laptop until a
  single-box site is specified, then on the board.
- **Shared:** the CO, a local OCI registry and `edgectl` on the laptop.

The same topology description drives the simulated version in CI (Site A only) and the bench
version (Sites A and B).

## Acceptance criteria
- One command brings the testbed up and another tears it down, both on Windows 11 (Docker
  Desktop) and in Linux CI.
- `edgectl` lists both sites and three hosts, each with the capabilities and labels it reports.
- The gateway-class EN in Site A reports an ARM architecture and its RAM cap. Placement (§8.6)
  never puts an over-capacity deployment there.
- Every §17.8 scenario runs against Site A in CI and passes.
- On the bench, the §17.8 golden path runs on Site B with a real ARM host.
- A short README states what each site models and how to add a host.

## Success metrics
- Every §17 bullet tagged as integration runs on the testbed (none skipped "for lack of
  environment").
- Bring-up time under 5 minutes on the laptop.

## Non-goals
- Real IoT devices or firmware (INT-003 covers simulated devices).
- k3s/Helm hosts, and performance or scale benchmarking.
- Fault injection beyond start and stop (INT-002).

## Assumptions
- Docker Desktop on the laptop can run ARM images under emulation.
- The BeagleBoard runs Linux with Docker or Podman and Compose, and is reachable on the LAN.

## Open questions
- Exact BeagleBoard model and CPU architecture (armv7 or arm64)?
- Is Site B's EN installed as a container or as a native service on the board?
