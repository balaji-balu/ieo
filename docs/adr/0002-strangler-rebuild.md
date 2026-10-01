# 0002. Rebuild the Phase 1 core from `SPEC.md` (strangler)

Status: Accepted · Date: 2026-09-30 · Spec: Appendix B · Supersedes: `docs/adr/era/001`, `002`

## Context
The architecture review (2026-09-30) found the current code differs from `SPEC.md` in its delivery
model (Git repo vs. Margo State Manifest), operation model (six fine-grained ops vs. whole-deployment
Apply/Remove), and runtime (mock containerd vs. Compose). The LO reconcile loop also has blocking
bugs: reconciling one deployment removes other apps, ops are sent once per alive host, and every app
goes to every host.

## Decision
- Build Phase 1 as **new packages** written from `SPEC.md`, one vertical slice at a time
  (`docs/roadmap.md`). Do not patch the Git-based path.
- **Keep** and adapt: ent + Atlas, the BoltDB single-writer loop (`internal/lo/boltstore`),
  `pkg/logx`, `internal/metrics`, the `edgectl` Cobra skeleton, Compose/Helm deploy files.
- **Delete** old code in the same PR that replaces it: `internal/git*`, `internal/gitobserver`,
  `internal/co`, `internal/lo/{watcher,reconciler,actuators}`, `internal/streammanager`, `proto/` and
  the unstarted gRPC server, and the ERA containerd/k3s/wasm/mock plugins.
- Old and new code never share types. New code does not import old packages.

## Consequences
The Git-based demo stops working once slice C lands; the golden path replaces it. Agents learn from
a codebase that matches the spec, which lowers review effort per PR.

## Options considered
- Incremental migration: every golden-path file would be rewritten anyway, while two models coexist.
