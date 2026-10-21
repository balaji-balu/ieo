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

Every `docker compose` command on this file needs two secrets (SPEC §15.6): `SITE_NATS_PASSWORD`,
the site's NATS password, which the NATS server, the LO and the ENs share, and `CO_OPERATOR_TOKEN`,
the bearer token of the CO's operator API (at least 32 bytes). The LO also needs its site's token,
which only the CO issues. The first start therefore makes the two secrets, starts the CO and adds
the site, then starts the rest. In PowerShell:

```powershell
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$bytes = New-Object byte[] 24; $rng.GetBytes($bytes)
"SITE_NATS_PASSWORD=" + -join ($bytes | ForEach-Object { $_.ToString('x2') }) | Set-Content -Encoding ascii deploy/dev/.env
$bytes = New-Object byte[] 32; $rng.GetBytes($bytes)
"CO_OPERATOR_TOKEN=" + -join ($bytes | ForEach-Object { $_.ToString('x2') }) | Add-Content -Encoding ascii deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --build --wait co
$token = docker compose -f deploy/dev/compose.yaml exec -T co app site add 00000000-0000-4000-8000-000000000001
"LO_SITE_TOKEN=$token" | Add-Content -Encoding ascii deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --build --wait
```

In bash:

```sh
echo "SITE_NATS_PASSWORD=$(openssl rand -hex 24)" > deploy/dev/.env
echo "CO_OPERATOR_TOKEN=$(openssl rand -hex 32)" >> deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --build --wait co
token=$(docker compose -f deploy/dev/compose.yaml exec -T co app site add 00000000-0000-4000-8000-000000000001)
echo "LO_SITE_TOKEN=$token" >> deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --build --wait
```

Compose reads `deploy/dev/.env`, which git ignores. The values stay there for later starts; the
site token works as long as the Postgres volume is kept. Without the password or the operator
token, compose stops with `required variable SITE_NATS_PASSWORD is missing a value` (or
`CO_OPERATOR_TOKEN`). The site ID is a UUID because the old CO
API, which runs beside the new one until roadmap slice E, takes only UUIDs.

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
| `co` | 9002, 8443, 9001 | Central Orchestrator: Margo API for LOs on 9002 (health check at `/healthz`); operator API for `edgectl` on 8443; the old API on 9001 until roadmap slice E |
| `lo` | — | Local Orchestrator for the one site |
| `nats` | 4222, 8222 (monitoring) | LO ↔ EN messaging |
| `postgres` | 5432 | CO database (`postgres` / `postgres`, database `orchestration`) |
| `registry` | 5000 | Application Registry (OCI) |
| `host1`, `host2` | — | A simulated host: its own Docker engine (`docker:dind`, privileged) |
| `en1`, `en2` | — | The EN of each host |

**Simulated hosts.** A host is two containers that act as one machine. `hostN` runs a Docker engine;
`enN` shares its network, so the EN reaches that engine at `tcp://127.0.0.1:2375` and cannot reach
the other host's. Workloads the EN starts run inside `hostN`, isolated from the laptop's own
containers and from the other host. Each EN keeps its host ID (`host-1`, `host-2`) and its store
in a volume (`en1-data`, `en2-data`), so a restart keeps both; `down -v` deletes them. The same
volume is mounted into `hostN` at the same path, `/var/lib/ieo/en`, so the paths of the files the
EN writes for a component mean the same to its engine. To look inside a host:

```sh
docker compose -f deploy/dev/compose.yaml exec host1 docker ps
```

**Current code.** CO, LO and EN are built from `cmd/co`, `cmd/lo` and `cmd/en`. The CO serves the
Margo API, and the LO polls it every 5 s (`LO_POLL_INTERVAL`; 60 s by default) and keeps the site's desired state in
`/var/lib/lo/lo.db`. Each EN takes the LO's commands and runs them with the Compose CLI on its
host's engine. The old, Git-based code still runs beside the CO and LO until roadmap slices D5.6c–E
replace it: they clone public GitHub repositories at startup (they log an error and keep running if
that fails).

The LO reaches the CO over plain HTTP on the compose network (`LO_CO_INSECURE=true`), so it logs a
warning at startup. It uses no HTTP proxy, whatever `HTTP_PROXY` and `HTTPS_PROXY` say (SPEC §15.6).

The LO also connects to the `nats` container as the site's user, without TLS
(`LO_NATS_INSECURE=true`, a second warning at startup), and asks the hosts for their inventories.
The ENs connect the same way, as `host-1` and `host-2`, and answer: each logs the same warning, and
one for `EN_REGISTRY_INSECURE=true`, since the `registry` container is plain HTTP (ADR 0021). The
NATS server takes only the site's login, `site` with `SITE_NATS_PASSWORD`. The LO has no health
endpoint, so `ps` shows it as running, not healthy.

## Adding a site

The Margo API on 9002 serves a site only to an LO that presents the site's token. `co site add`
prints the token once; the CO stores only its hash. The harness's LO uses the site added in the
first start (above).

If the token is lost, or `deploy/dev/.env` was deleted while the Postgres volume was kept,
`site add` refuses the existing site. Print a new token instead, which replaces the old one, then
restart the LO (PowerShell):

```powershell
$token = docker compose -f deploy/dev/compose.yaml exec -T co app site rotate-token 00000000-0000-4000-8000-000000000001
(@(Get-Content deploy/dev/.env) -notmatch '^LO_SITE_TOKEN=') + "LO_SITE_TOKEN=$token" | Set-Content -Encoding ascii deploy/dev/.env
docker compose -f deploy/dev/compose.yaml up -d --wait lo
```

The second line keeps `SITE_NATS_PASSWORD` and replaces only the token.

## The operator API

The CO serves the operator API (SPEC §11.3) on 8443, over plain HTTP on the laptop, to any request
that carries `CO_OPERATOR_TOKEN` from `deploy/dev/.env`; any other gets `401`. `edgectl` will call
it (roadmap E5). Until then, read it with curl, for example the devices the LO reported
(PowerShell):

```powershell
$op = (Select-String -Path deploy/dev/.env -Pattern '^CO_OPERATOR_TOKEN=(.*)').Matches[0].Groups[1].Value
curl.exe -s -H "Authorization: Bearer $op" http://localhost:8443/api/v1/devices
```

A deployment's status is at `/api/v1/deployments/<id>/status`, and the imported apps at
`/api/v1/apps`. To change the token, edit `deploy/dev/.env` and run `up -d --wait co`.

## Using the registry

Push packages and images from the laptop to `localhost:5000`; the hosts pull the same content as
`registry:5000` (plain HTTP, allowed for this name only):

```sh
docker tag hello-world localhost:5000/hello-world:1.0
docker push localhost:5000/hello-world:1.0
docker compose -f deploy/dev/compose.yaml exec host1 docker pull registry:5000/hello-world:1.0
```

## Deploying the test application

`tools/harnessdeploy` deploys the test application in `tests/harness/hello` (one Compose component,
`web`, running busybox) to `host-1`, as CI's "Dev harness" job does. It copies busybox, pinned by
digest, from Docker Hub into the registry, so it needs internet access. It pushes the
application's Compose archive and package to `localhost:5000`, imports the application into the
CO's Postgres and creates a directed deployment to `00000000-0000-4000-8000-000000000001/host-1`.
Until roadmap slice E brings `edgectl deploy`, this tool is the only way to create a deployment.
Until slice H, it also records the capabilities of the site's gateway and of `host-1`, which an
LO would report: the CO creates a deployment only to a host it knows.

With the stack up (above), from the repository root in PowerShell:

```powershell
$env:DATABASE_URL = "postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable"
go run ./tools/harnessdeploy | Tee-Object -Variable out
$project = ($out | Select-String '^COMPOSE_PROJECT=(.*)').Matches.Groups[1].Value
docker compose -f deploy/dev/compose.yaml exec -T host1 docker ps --filter "label=com.docker.compose.project=$project"
docker compose -f deploy/dev/compose.yaml exec -T host2 docker ps -a --filter label=com.docker.compose.project
docker compose -f deploy/dev/compose.yaml logs en1 | Select-String '"component installed"'
$id = ($out | Select-String '^DEPLOYMENT_ID=(.*)').Matches.Groups[1].Value
docker compose -f deploy/dev/compose.yaml exec -T postgres psql -U postgres -d orchestration -tAc "SELECT convert_from(status, 'UTF8') FROM co_statuses WHERE deployment_id = '$id' ORDER BY id DESC LIMIT 1"
```

The tool prints `DEPLOYMENT_ID=<id>` and `COMPOSE_PROJECT=<project>`. Within seconds (the LO
polls every 5 s in the harness) `host1` runs one container of the project and `host2` runs
none, and `en1` logs `component installed` with the deployment ID. The last query prints the
latest DeploymentStatus the LO sent the CO: `installed`, from `deviceId` `<site>/host-1`, with
one entry for `web` (until roadmap step E6 brings `edgectl deployment status`, the CO's Postgres
is where to read it). Running the tool again creates
no second deployment: it prints the first. To check that an EN restart leaves the container
alone, note its ID, then:

```powershell
docker compose -f deploy/dev/compose.yaml restart en1
Start-Sleep 15
docker compose -f deploy/dev/compose.yaml exec -T host1 docker ps -q --filter "label=com.docker.compose.project=$project"
```

The ID is the same. `down -v` deletes the deployment with everything else.

## Only NATS and Postgres

To run CO, LO and EN with `go run` instead (see [`contributing.md`](contributing.md#get-started)):

```sh
docker compose -f deploy/dev/compose.yaml up -d --wait nats postgres
```

It needs `SITE_NATS_PASSWORD` in `deploy/dev/.env`, as every compose command does (Start, inspect,
stop), and the `go run` LO and EN take the same value as `LO_NATS_PASSWORD` and `EN_NATS_PASSWORD`.

Don't run the full stack at the same time: the `go run` services use the same ports.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `Bind for 0.0.0.0:5432 failed: port is already allocated` (or 4222, 5000, 8443, 9001, 9002) | Another container or local service uses the port. Stop it, or stop an old stack: `docker ps`, then `docker stop <name>` |
| `up --wait` times out on `host1` or `host2` | Docker Desktop must allow privileged containers (the default). Check `docker compose -f deploy/dev/compose.yaml logs host1` |
| Containers killed or very slow | Not enough memory for two engines; raise the WSL 2 memory limit (see Prerequisites) |
| LO: `CO rejected: {"error":"db query failed"}` | The Postgres volume predates the current schema; reset it with `down -v` (deletes data) |
| `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` | No C compiler; install gcc (for example MSYS2 `mingw-w64-ucrt-x86_64-gcc`) or run the tests without `-race` (see Prerequisites) |
| golangci-lint: `the Go language version (go1.24) used to build golangci-lint is lower than the targeted Go version (1.25)` | Reinstall it with `go install` as in Prerequisites |
| `gofmt -l` or golangci-lint reports `File is not properly formatted (gofmt)` for many files you didn't touch | The clone predates `.gitattributes`, so its working copies are still CRLF, and gofmt reports every CRLF file. Commit or stash your work, then check everything out again with LF: `git rm -r --cached -q .` followed by `git reset --hard`. New clones need nothing: `.gitattributes` checks Go files out with LF whatever `core.autocrlf` says |
| Merging or rebasing onto `main` reports a legacy Go file as conflicting on every line | The branch predates `.gitattributes` and edits a file that was stored with CRLF. Abort, then repeat with `-X renormalize` (`git merge -X renormalize origin/main`, or `git rebase -X renormalize origin/main`) |
| `en1` or `en2` keeps restarting | It exits 2 on an invalid configuration and 1 when it cannot start, for example when `docker compose version` does not run; its last log line says which. Check `logs en1` |
| `lo` keeps restarting, its log says `LO_SITE_TOKEN (lo.site_token) is not set` | Add the site and write `deploy/dev/.env` (Start, inspect, stop) |
| `required variable SITE_NATS_PASSWORD is missing a value` | `deploy/dev/.env` has no `SITE_NATS_PASSWORD`; add one (Start, inspect, stop). Changing it needs `up -d` of `nats`, `lo`, `en1` and `en2` together |
| `required variable CO_OPERATOR_TOKEN is missing a value` | `deploy/dev/.env` predates the operator API: add the `CO_OPERATOR_TOKEN` line from Start, inspect, stop, then `up -d --wait co` |
| `co` keeps restarting, its log says `CO_OPERATOR_TOKEN is shorter than 32 bytes` | Make a longer one, as in Start, inspect, stop |
| LO: `"outcome":"Unreachable"` with `status 401` | The token in `deploy/dev/.env` is not the site's: rotate it (Adding a site) |
