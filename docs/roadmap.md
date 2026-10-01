# Roadmap — Intelligent Edge Orchestrator (ieo)

Status: v1 · Date: 2026-09-30 · Decisions: `docs/adr/` · Process: `docs/process.md`

Phase 1 is one vertical slice of the product: **deploy a Compose application from the CO to a
host at a site, keep it converged through outages, and report its status**, conforming to
`SPEC.md` §17.1–§17.8. Everything runs and is tested on one Windows 11 laptop (ADR 0009).
Work is built as new packages from the spec, replacing the old Git-based code (ADR 0002).

## How the work flows

1. Take the next PR in order below and open an issue for it: goal, § bullets, out of scope.
2. One Claude Code session per PR: plan → approval → failing tests → code → local checks → PR.
3. The maintainer reviews and merges. Then the next PR.

Each PR is about 15 minutes of review and changes one slice. Stages collapse as in ADR 0007
when the § bullets already exist.

## Milestone 0 — Foundations

| PR | Scope | Done when |
| --- | --- | --- |
| 0.1 | ADRs 0001–0009, this roadmap, Appendix B reordered, process updated | Merged |
| 0.2 | Branding and rename (ADR 0001, 0008): module path, `cmd/era` → `cmd/en`, image and chart names, developer README, remove the committed `edgectl` binary, fix `relaase.yaml`, `go.mod` → `go 1.25` | `go build` of renamed packages works; no `margo-hello-world` left outside history |
| 0.3 | Move data plane to `docs/proposals/data-plane.md` (ADR 0006) | SPEC and overview point to the proposal |
| 0.4 | Agent enablers: `.github/workflows/ci.yaml` (build, vet, lint, `go test -race` on changed packages), PR template, conformance tool `go run ./tools/conformance` (lists §17 bullets without a `TestSpec_` test), `.claude/settings.json` SessionStart hook | CI runs on a PR; conformance tool prints the gap list |
| 0.5 | Laptop harness (ADR 0009): `deploy/dev/compose.yaml` with NATS, Postgres, `registry:2`, CO, LO and two `docker:dind` hosts; `docs/dev-setup.md` for Windows | `docker compose -f deploy/dev/compose.yaml up` starts all services on the laptop |

## Milestone 1 — Golden path v0: directed deploy to one host

Goal: `edgectl` imports an app, deploys it to a named host, and sees `installed`.
§17.8 golden path, first three steps.

| PR | Slice | Spec | Packages |
| --- | --- | --- | --- |
| A | Contract types: Margo rc.3 types, site messages, JSON schemas, identifiers; fake clock, registry and runtime for tests | §4.2, §11, §17.1 | `internal/contract`, `internal/platform` |
| B | CO: import app from OCI registry; create directed deployment; store YAML by digest; per-site manifest with ETag; site token auth (ADR 0005) | §5.3, §8.1, §11.1, §16.1, §17.2 (create, manifest, ETag, 304, site scoping) | `internal/co/{catalog,deploy,manifest,api,store}` |
| C | LO: sync loop — poll, rollback protection, digest verification, durable desired state; replaces the Git watcher | §8.2, §16.3, §17.3 (first sync, 304, rollback, digest mismatch, restart) | `internal/lo/{sync,store}` |
| D | LO plan + EN execute: pure planner over all deployments; EN Apply/Remove via Compose CLI (ADR 0003); inventory; NATS credentials | §8.5, §8.9, §16.4, §16.6, §17.4 (diff rules), §17.6 (idempotency, order, archive safety) | `internal/lo/plan`, `internal/en/{exec,compose,store,report}` |
| E | Status to CO and `edgectl`: mapping, `edgectl deploy` and `status`; golden-path integration test on the laptop harness | §7.2, §8.7, §10, §11.3, §17.5 (mapping), §17.8 bullet 1 part 1 | `internal/lo/outbox` (send only), `cmd/edgectl` |

After E, delete the Git-based code paths still left (ADR 0002).

## Milestone 2 — Golden path complete

| PR | Slice | Spec |
| --- | --- | --- |
| F | Update: new version → new digest → `installed` at new digest; removed components cleaned up | §17.2 update, §17.6 update |
| G | Delete: deployment removed → `removed`; status history kept | §17.2 delete, §17.3 absent deployment |
| H | Autonomous placement: host with most free memory; capability reporting | §8.3, §8.4, §8.6, §16.5, §17.4 placement, §17.8 bullet 2 |
| I | Host liveness: offline after missed heartbeats, `pending`, reconcile on return | §7.3, §17.4 offline/online |

## Milestone 3 — Site autonomy and recovery

| PR | Slice | Spec |
| --- | --- | --- |
| J | Durable status outbox: latest per deployment, survives restart, flush on reconnect | §8.8, §17.5 outbox |
| K | Retry and backoff for commands; `Retry-After`, polling hours, downtime windows | §17.3, §17.4 retries |
| L | Recovery: LO restart with store intact and deleted; EN restart; container crash | §14, §17.6 restart, §17.8 bullets 3–6 |
| M | Observability: §13.1 log fields, §13.2 metrics, OpenTelemetry collector on hosts | §9.3, §13, §17.7 metrics |

## Milestone 4 — Security (Appendix B step 4)

| PR | Slice | Spec |
| --- | --- | --- |
| N | mTLS with SPIFFE IDs on CO ↔ LO; `edgectl site add` issues certificates; interim tokens removed | §15.2, §17.7 |
| O | Scoped NATS credentials per host; security review of §15 areas | §15.1, §17.7 |

## Phase 1 release — v0.1.0

- `go run ./tools/conformance` reports every §17.1–§17.7 bullet covered.
- §17.8 passes on the laptop harness and in CI.
- §18.1 checklist ticked. Tag `v0.1.0`; release images and `edgectl` binaries (Windows, Linux).

## Next proposals (after v0.1.0)

Each enters as a proposal → spec section → slices, the same way.

1. **CO on k3s:** run the CO (and optionally LO) on k3s with the Helm charts in `deploy/helm/`.
2. **Helm deployment type:** EN deploys Helm charts to k3s hosts (SPEC §2.2 non-goal today).
3. **Data plane:** `docs/proposals/data-plane.md` (ADR 0006).
4. **Intelligent placement:** labels, spreading, GPUs, model affinity — advisory, deterministic fallback.
5. **Web portal and multi-tenancy.**
