# Laptop harness

The whole IEO stack on one Windows 11 laptop: 1 CO, 1 LO and 2 simulated hosts, with NATS,
Postgres and a local OCI registry (ADR 0009). It is the environment for the §17.8 Site Integration
Profile, and CI starts the same stack on every PR (job "Dev harness").

Every command below runs from the repository root and works the same in PowerShell and bash,
except where a block is marked PowerShell.

## Prerequisites

| Tool | Version | Notes |
| --- | --- | --- |
| Docker Desktop | current, WSL 2 backend | Podman Desktop also works but is not the default |
| Go | 1.25 | For `go build`, `go test` and the helpers under `tools/` |
| Git | any | |
| golangci-lint | v2.5.0, built with Go 1.25 | Same version as CI: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0`. A release binary or an install built with an older Go refuses to lint a Go 1.25 module |
| C compiler (gcc) | any, optional | Only for `go test -race`, which needs cgo. Without one on Windows, drop `-race` locally; CI runs it on Linux |

Memory: give Docker Desktop at least 6 GB (Settings → Resources, or `memory=` in `%UserProfile%\.wslconfig`).
Each simulated host runs its own Docker engine and needs about 1–2 GB.

## Start, inspect, stop

The LO needs its site's token, which only the CO issues. The first start therefore has two steps:
start the CO and add the site, then start the rest. In PowerShell:

```powershell
docker compose -f deploy/dev/compose.yaml up -d --build --wait co
$token = docker compose -f deploy/dev/compose.yaml exec -T co app site add 00000000-0000-4000-8000-000000000001
"LO_SITE_TOKEN=$token" | Set-Content -Encoding ascii deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --build --wait
```

In bash, lines 2 and 3 are:

```sh
token=$(docker compose -f deploy/dev/compose.yaml exec -T co app site add 00000000-0000-4000-8000-000000000001)
echo "LO_SITE_TOKEN=$token" > deploy/dev/.env
```

Compose reads `deploy/dev/.env`, which git ignores; the token stays there for later starts, as long
as the Postgres volume is kept. The site ID is a UUID because the old LO, which still runs beside the
new one, registers with the old CO API, and that API takes only UUIDs (until roadmap slice E).

```sh
docker compose -f deploy/dev/compose.yaml up -d --build --wait   # later starts: build CO, LO, EN from this checkout and start
docker compose -f deploy/dev/compose.yaml ps                     # all services "running" or "healthy"
docker compose -f deploy/dev/compose.yaml logs -f lo en1         # follow some logs
docker compose -f deploy/dev/compose.yaml down                   # stop, keep data
docker compose -f deploy/dev/compose.yaml down -v                # stop and delete all data
```

`--build` rebuilds CO, LO and EN after you change Go code. The first build takes a few minutes;
later ones reuse the cache. After `down -v` the site and its token are gone: start again with the
two steps above.

The LO logs one line per sync attempt. To see them:

```sh
docker compose -f deploy/dev/compose.yaml logs lo | Select-String outcome   # PowerShell; grep outcome in bash
```

## What runs

| Service | Laptop port | Role |
| --- | --- | --- |
| `co` | 9002, 9001 | Central Orchestrator: Margo API for LOs on 9002 (health check at `/healthz`); the old API on 9001 until roadmap slice E |
| `lo` | 9010 | Local Orchestrator for the one site |
| `nats` | 4222, 8222 (monitoring) | LO ↔ EN messaging |
| `postgres` | 5432 | CO database (`postgres` / `postgres`, database `orchestration`) |
| `registry` | 5000 | Application Registry (OCI) |
| `host1`, `host2` | — | A simulated host: its own Docker engine (`docker:dind`, privileged) |
| `en1`, `en2` | — | The EN of each host |

**Simulated hosts.** A host is two containers that act as one machine. `hostN` runs a Docker engine;
`enN` shares its network, so the EN reaches that engine at `tcp://127.0.0.1:2375` and cannot reach
the other host's. Workloads the EN starts run inside `hostN`, isolated from the laptop's own
containers and from the other host. The EN keeps no state across restarts: each start registers
with a new host ID, and the CO keeps a row for every old one. To look inside a host:

```sh
docker compose -f deploy/dev/compose.yaml exec host1 docker ps
```

**Current code.** CO, LO and EN are built from `cmd/co`, `cmd/lo` and `cmd/en`. The CO serves the
Margo API, and the LO polls it every 60 s and keeps the site's desired state in
`/var/lib/lo/lo.db`. The old, Git-based code still runs beside them until roadmap slices C3b–E
replace it: the CO and LO clone public GitHub repositories at startup (they log an error and keep
running if that fails), and the EN uses a mock runtime, so it does not start workloads in its host
yet.

The LO reaches the CO over plain HTTP on the compose network (`LO_CO_INSECURE=true`), so it logs a
warning at startup. It uses no HTTP proxy, whatever `HTTP_PROXY` and `HTTPS_PROXY` say (SPEC §15.6).

## Adding a site

The Margo API on 9002 serves a site only to an LO that presents the site's token. `co site add`
prints the token once; the CO stores only its hash. The harness's LO uses the site added in the
first start (above).

If the token is lost, or `deploy/dev/.env` was deleted while the Postgres volume was kept,
`site add` refuses the existing site. Print a new token instead, which replaces the old one, then
restart the LO (PowerShell):

```powershell
$token = docker compose -f deploy/dev/compose.yaml exec -T co app site rotate-token 00000000-0000-4000-8000-000000000001
"LO_SITE_TOKEN=$token" | Set-Content -Encoding ascii deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --wait lo
```

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
| `Bind for 0.0.0.0:5432 failed: port is already allocated` (or 4222, 5000, 9001, 9002, 9010) | Another container or local service uses the port. Stop it, or stop an old stack: `docker ps`, then `docker stop <name>` |
| `up --wait` times out on `host1` or `host2` | Docker Desktop must allow privileged containers (the default). Check `docker compose -f deploy/dev/compose.yaml logs host1` |
| Containers killed or very slow | Not enough memory for two engines; raise the WSL 2 memory limit (see Prerequisites) |
| LO: `CO rejected: {"error":"db query failed"}` | The Postgres volume predates the current schema; reset it with `down -v` (deletes data) |
| `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` | No C compiler; install gcc (for example MSYS2 `mingw-w64-ucrt-x86_64-gcc`) or run the tests without `-race` (see Prerequisites) |
| golangci-lint: `the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.25)` | Reinstall it with `go install` as in Prerequisites |
| `gofmt -l` or golangci-lint reports `File is not properly formatted (gofmt)` for files you didn't format by hand | With `core.autocrlf`, Git writes CRLF working copies (after a checkout or rebase), and gofmt reports every CRLF file, which can hide a real formatting error that CI (Linux, LF) then fails on. Check what you committed instead: list the changed files with `git diff --name-only origin/main...HEAD -- "*.go"`, then run `git show HEAD:<file> \| gofmt -l` for each; it prints `<standard input>` only for a file that really needs `gofmt -w` |
| `en1` or `en2` keeps restarting | It registers with the LO once at startup and exits on failure; check `logs lo en1` |
| `lo` keeps restarting, its log says `LO_SITE_TOKEN (lo.site_token) is not set` | Add the site and write `deploy/dev/.env` (Start, inspect, stop) |
| LO: `"outcome":"Unreachable"` with `status 401` | The token in `deploy/dev/.env` is not the site's: rotate it (Adding a site) |
