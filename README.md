<p align="center">
  <img src="docs/logo.png" alt="Intelligent Edge Orchestrator logo" width="150"/>
</p>

# Intelligent Edge Orchestrator (ieo)

![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)

**ieo** deploys and manages containerized applications across many **sites** (factories, stores,
plants), each with a handful of **hosts**, over networks that are slow, metered or intermittently
unavailable. It aims to be **Margo-conformant**: it implements the
[Margo](https://specification.margo.org/) specification for edge application orchestration.

Three tiers:

- **CO** (Central Orchestrator): the control plane. Operators import applications and create
  deployments through the CO, using `edgectl`.
- **LO** (Local Orchestrator, one per site): pulls the site's desired state from the CO, places
  workloads on hosts and keeps them converged while the site is offline.
- **EN** (Edge Node, one per host): runs workloads as Compose projects and reports their status.

> **Status:** Phase 1 is being rebuilt from [`SPEC.md`](SPEC.md) in vertical slices
> ([`docs/roadmap.md`](docs/roadmap.md), [ADR 0002](docs/adr/0002-strangler-rebuild.md)). The
> current Git-based code is replaced slice by slice.

## Documentation

| Document | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | Implementation contract: MUST/SHOULD rules, §17 tests, §18 checklist |
| [`docs/system-overview.md`](docs/system-overview.md) | How the system works and why |
| [`docs/roadmap.md`](docs/roadmap.md) | Ordered slices for Phase 1 |
| [`docs/adr/`](docs/adr/README.md) | Architecture decisions |
| [`docs/intents/`](docs/intents/README.md) | Why a change is worth doing |
| [`docs/process.md`](docs/process.md) | How changes are made (AI-native SDLC) |
| [`docs/contributing.md`](docs/contributing.md) | Contributor setup, schema changes |
| [`CLAUDE.md`](CLAUDE.md) | Rules for coding agents |

## Developer quick start

Prerequisites: Go 1.25, Git, and Docker Desktop (or Podman Desktop). The commands below work the
same in PowerShell and bash.

```sh
git clone https://github.com/balaji-balu/ieo.git
cd ieo

go build ./cmd/...        # co, lo, en, edgectl
go test ./...
```

Run the services against a local NATS and Postgres (configure `.env` first; see
[`docs/contributing.md`](docs/contributing.md#get-started)):

```sh
docker compose -f docker-compose.dev.yaml up -d
atlas migrate apply --env local     # create the CO schema once; no Atlas? see docs/contributing.md
go run ./cmd/co
go run ./cmd/lo
go run ./cmd/en
go run ./cmd/edgectl --help
```

Known baseline issues are listed in [`CLAUDE.md`](CLAUDE.md#commands).

## Names

| Thing | Name |
| --- | --- |
| Go module | `github.com/balaji-balu/ieo` |
| Binaries | `ieo-co`, `ieo-lo`, `ieo-en`, `edgectl` |
| Images | `ghcr.io/balaji-balu/ieo-co`, `ieo-lo`, `ieo-en` |
| Helm charts | `deploy/helm/ieo-co`, `deploy/helm/ieo-lo` |
| EN environment variables | `IEO_EN_*` |

See [ADR 0001](docs/adr/0001-name-and-module-path.md) and
[ADR 0008](docs/adr/0008-rename-era-to-en.md).

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/co`, `cmd/lo`, `cmd/en`, `cmd/edgectl` | Entry points |
| `internal/co`, `internal/lo`, `internal/en` | Component internals |
| `pkg/` | Shared types and logging |
| `ent/`, `db/`, `atlas.hcl` | Persistence: ent schema (generated code) and Atlas migrations |
| `configs/` | Component configs |
| `deploy/` | Compose and Helm deployment |
| `tests/` | Fixtures and end-to-end tests |

## Contributing

See [`docs/contributing.md`](docs/contributing.md) and [`docs/process.md`](docs/process.md).
Bug reports, feature requests, tests and documentation improvements are welcome.

<a href="https://github.com/balaji-balu/ieo/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=balaji-balu/ieo" />
</a>

## License

MIT. See [LICENSE](LICENSE).
