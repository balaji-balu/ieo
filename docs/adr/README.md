# Architecture Decision Records

Decisions on anything `SPEC.md` leaves open or calls "implementation-defined". Order of authority:
Margo → `SPEC.md` → these ADRs → code. Agents follow an **Accepted** ADR without re-asking; to change
one, write a new ADR that supersedes it.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-name-and-module-path.md) | Project name `ieo` and module path | Accepted |
| [0002](0002-strangler-rebuild.md) | Rebuild the Phase 1 core from `SPEC.md` (strangler) | Accepted |
| [0003](0003-en-runs-compose-via-cli.md) | EN runs Compose through the CLI behind an interface | Accepted |
| [0004](0004-co-persistence-ent.md) | CO keeps ent + Atlas on Postgres | Accepted |
| [0005](0005-interim-auth.md) | Interim token auth until mTLS | Accepted |
| [0006](0006-data-plane-out-of-phase-1.md) | Data plane moves out of Phase 1 docs | Accepted |
| [0007](0007-lighter-loop-for-specified-work.md) | Lighter SDLC loop for already-specified work | Accepted |
| [0008](0008-rename-era-to-en.md) | Rename ERA to EN in code | Accepted |
| [0009](0009-laptop-test-environment.md) | Laptop test environment (Windows 11, simulated hosts) | Accepted |
| [0010](0010-margo-wm-api-pin-and-conformance.md) | Margo client API: stay on pinned rc.3, adopt a pin-change procedure | Accepted |
| [0011](0011-spec-test-traceability.md) | How a `TestSpec_` test names the §17 bullet it covers | Proposed |
| [0012](0012-deployment-bundle-layout.md) | Deployment bundle layout and deterministic encoding | Proposed |
| [0013](0013-co-store-schema-and-migrations.md) | CO store: own ent schema, embedded SQL migrations applied on open (refines 0004) | Proposed |
| [0014](0014-lo-store-layout.md) | LO store: new bbolt store and its file layout (refines 0002) | Proposed |
| [0015](0015-lo-store-hosts-and-actual.md) | LO store: `hosts` and `actual` buckets, layout version 2 (refines 0014) | Proposed |
| [0016](0016-en-store-layout.md) | EN store: host ID file and a bbolt store for applied deployments | Proposed |
| [0017](0017-en-compose-cli.md) | EN Compose CLI: commands, start timeout and environment (refines 0003) | Proposed |
| [0018](0018-en-archive-extraction.md) | EN archive extraction: staging, containment and links (refines 0009) | Proposed |
| [0019](0019-en-pull.md) | EN pull: spool file, what is read and what counts as a mismatch | Proposed |
| [0020](0020-site-nats-client.md) | Site NATS client: one package, endless reconnect, nothing kept while disconnected (refines 0005) | Proposed |
| [0021](0021-en-registry-access.md) | EN registry access: Docker `config.json` credentials, HTTPS unless told otherwise (refines 0019) | Proposed |

Older design notes in `docs/adr/era/` are superseded by 0002 and 0003 and kept for history.

## Template

```markdown
# NNNN. <Decision in a few words>

Status: Proposed | Accepted | Superseded by NNNN · Date: YYYY-MM-DD · Spec: §x.y

## Context
What forces the decision. Cite SPEC sections.

## Decision
What we do, stated as rules an agent can follow.

## Consequences
What gets easier, what gets harder, what to watch.

## Options considered
- Option — why not.
```
