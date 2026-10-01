# Laptop harness

The whole IEO stack on one Windows 11 laptop: 1 CO, 1 LO and 2 simulated hosts, with NATS,
Postgres and a local OCI registry (ADR 0009). It is the environment for the §17.8 Site Integration
Profile, and CI starts the same stack on every PR (job "Dev harness").

Every command below runs from the repository root and works the same in PowerShell and bash.

## Prerequisites

| Tool | Version | Notes |
| --- | --- | --- |
| Docker Desktop | current, WSL 2 backend | Podman Desktop also works but is not the default |
| Go | 1.25 | For `go build`, `go test` and the helpers under `tools/` |
| Git | any | |

Memory: give Docker Desktop at least 6 GB (Settings → Resources, or `memory=` in `%UserProfile%\.wslconfig`).
Each simulated host runs its own Docker engine and needs about 1–2 GB.

## Start, inspect, stop

```sh
docker compose -f deploy/dev/compose.yaml up -d --build --wait   # build CO, LO, EN from this checkout and start
docker compose -f deploy/dev/compose.yaml ps                     # all services "running" or "healthy"
docker compose -f deploy/dev/compose.yaml logs -f lo en1         # follow some logs
docker compose -f deploy/dev/compose.yaml down                   # stop, keep data
docker compose -f deploy/dev/compose.yaml down -v                # stop and delete all data
```

`--build` rebuilds CO, LO and EN after you change Go code. The first build takes a few minutes;
later ones reuse the cache.

## What runs

| Service | Laptop port | Role |
| --- | --- | --- |
| `co` | 9001 | Central Orchestrator API |
| `lo` | 9010 | Local Orchestrator for the one site |
| `nats` | 4222, 8222 (monitoring) | LO ↔ EN messaging |
| `postgres` | 5432 | CO database (`postgres` / `postgres`, database `orchestration`) |
| `registry` | 5000 | Application Registry (OCI) |
| `host1`, `host2` | — | A simulated host: its own Docker engine (`docker:dind`, privileged) |
| `en1`, `en2` | — | The EN of each host |

**Simulated hosts.** A host is two containers that act as one machine. `hostN` runs a Docker engine;
`enN` shares its network, so the EN reaches that engine at `tcp://127.0.0.1:2375` and cannot reach
the other host's. Workloads the EN starts run inside `hostN`, isolated from the laptop's own
containers and from the other host. To look inside a host:

```sh
docker compose -f deploy/dev/compose.yaml exec host1 docker ps
```

**Current code.** CO, LO and EN are built from `cmd/co`, `cmd/lo` and `cmd/en` as they are today:
the Git-based code that roadmap slices B–D replace. Until then the CO and LO clone public GitHub
repositories at startup (they log an error and keep running if that fails), and the EN uses a mock
runtime, so it does not start workloads in its host yet. The harness picks up each slice as it
lands, with no change to the Compose file.

## Using the registry

Push packages and images from the laptop to `localhost:5000`; the hosts pull the same content as
`registry:5000` (plain HTTP, allowed for this name only):

```sh
docker tag hello-world localhost:5000/hello-world:1.0
docker push localhost:5000/hello-world:1.0
docker compose -f deploy/dev/compose.yaml exec host1 docker pull registry:5000/hello-world:1.0
```

## Only NATS and Postgres

To run CO, LO and EN with `go run` instead (see [`contributing.md`](contributing.md#get-started)):

```sh
docker compose -f deploy/dev/compose.yaml up -d --wait nats postgres
```

Don't run the full stack at the same time: the `go run` services use the same ports.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `Bind for 0.0.0.0:5432 failed: port is already allocated` (or 4222, 5000, 9001, 9010) | Another container or local service uses the port. Stop it, or stop an old stack: `docker ps`, then `docker stop <name>` |
| `up --wait` times out on `host1` or `host2` | Docker Desktop must allow privileged containers (the default). Check `docker compose -f deploy/dev/compose.yaml logs host1` |
| Containers killed or very slow | Not enough memory for two engines; raise the WSL 2 memory limit (see Prerequisites) |
| LO: `CO rejected: {"error":"db query failed"}` | The Postgres volume predates the current schema; reset it with `down -v` (deletes data) |
| `en1` or `en2` keeps restarting | It registers with the LO once at startup and exits on failure; check `logs lo en1` |
