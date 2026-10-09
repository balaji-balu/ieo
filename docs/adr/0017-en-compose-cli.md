# 0017. EN Compose CLI: commands, start timeout and environment

Status: Proposed · Date: 2026-10-09 · Spec: §5.4, §6.3, §8.9, §9.2, §15.6 · Refines: 0003

## Context
ADR 0003 has the EN run `docker compose` or `podman compose` behind one interface. Roadmap slice
D3 (balaji-balu/ieo#58) builds it. The spec leaves these points open:
- the default `en.runtime` (§6.3: implementation-defined);
- the exact commands;
- how a start timeout is told apart from other failures. Compose exits non-zero for both, so the
  exit status can't tell them apart;
- which environment the CLI gets. §15.6 says it holds no tier credential, "only what the CLI needs
  to reach the engine", but doesn't list it.

## Decision
- The interface is `compose.Runner` with `Services`, `Up` and `Down`, with `ctx` on each. ADR 0003
  also names `List`; it is added by roadmap L, its first consumer.
- `en.runtime` defaults to `docker`. The CLI is `<runtime> compose`; startup runs
  `<runtime> compose version` and fails if it does.
- Commands, for project `<name>` with files `compose.yaml` and `compose.ieo.yaml` (SPEC §5.4):
  - `compose -p <name> -f compose.yaml -f compose.ieo.yaml --project-directory <dir> config --services`;
  - `… up -d`, plus `--wait --wait-timeout <seconds, rounded up>` when `wait` is true;
  - `compose -p <name> down --remove-orphans`. It never passes `-v`: named volumes are workload
    data, and no spec rule deletes them.
- The project name must match `[a-z0-9][a-z0-9_-]*`, the form `contract.ComposeProjectName`
  produces (SPEC §4.2). Any other name is refused before a command runs (SPEC §9.2).
- Start timeout: with `wait`, the whole `up` runs under a deadline of `timeout`.
  - If it fails once the timeout has elapsed, the cause is `ErrStartTimeout`, which the EN reports
    as `IEO-START-TIMEOUT`. A failure before the timeout is `IEO-COMPOSE-FAILED`.
  - Without `wait`, `timeout` is ignored. `timeout` 0 means no deadline.
- Environment: the CLI gets only the variables below, compared without regard to case. Everything
  else is dropped, including every `en.*` setting, NATS credentials, the site token and proxy
  variables:
  - engine: `DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`, `DOCKER_CERT_PATH`,
    `DOCKER_TLS_VERIFY`, `CONTAINER_HOST`, `CONTAINER_CONNECTION`, `CONTAINERS_CONF`,
    `XDG_RUNTIME_DIR`;
  - OS: `PATH`, `HOME`, `TMPDIR`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`,
    `ProgramFiles`, `SystemRoot`, `SystemDrive`, `windir`, `PATHEXT`, `ComSpec`, `TEMP`, `TMP`.
- A failed command's error names the project and carries the command's combined output, capped at
  64 KiB. The EN doesn't log the output separately.

## Consequences
- `${…}` references in an archive's `compose.yaml` can't read EN settings or credentials. They see
  only the variables above, plus what the EN writes to `compose.ieo.yaml`.
- A host that reaches its registry or engine only through a proxy must configure the proxy in
  the engine (Docker daemon or Podman settings), not in the EN's environment.
- Image pulls count against `timeout` when `wait` is true, because the deadline covers the whole
  `up`.
- Podman's `compose` delegates to an external provider. `--wait` works only with providers that
  support it; the laptop harness uses Docker.

## Options considered
- Pass the EN's environment minus a denylist of `EN_*`/`IEO_*` names: a credential under any other
  name would leak. §15.6 asks for an allowlist ("only what the CLI needs").
- Rely on `--wait-timeout` alone and read Compose's message to find a timeout: the message isn't a
  stable interface, and a pull that hangs before the wait would never end.
- Run `down -v` to leave nothing behind: it deletes workload data on every Remove and every update
  that drops a component.
