# Intelligent Edge Orchestrator Specification

Status: Draft v1 (language-agnostic)

Purpose: Define a system that deploys and keeps containerized applications running across many
edge sites, each with a few hosts, over networks that are slow, metered or intermittently
unavailable.

Margo baseline: Margo Specification pre-draft, Workload Management API `1.0.0-rc.3`
([margo/specification](https://github.com/margo/specification), commit `f209a7f`). The pinned
OpenAPI file is kept unchanged at `api/margo/f209a7f/workload-management-api-1.0.0-rc.3.yaml`; a pin
change follows §17.1 (ADR 0010). The Application Description schema (§5.3) is pinned in the same
directory: Margo's LinkML source and the JSON Schema generated from it (see the README there).
The ApplicationDeployment layout (§4.1.5) follows Margo's `DesiredState` schema at the same commit
(`src/specification/margo-management-interface/desired-state.linkml.yaml`).

## Normative Language

The key words `MUST`, `MUST NOT`, `REQUIRED`, `SHOULD`, `SHOULD NOT`, `RECOMMENDED`, `MAY`, and
`OPTIONAL` in this document are to be interpreted as described in RFC 2119.

`Implementation-defined` means the behavior is part of the implementation contract, but this
specification does not prescribe one universal policy. Implementations MUST document the selected
behavior.

`[Margo]` marks a rule this document repeats from the Margo specification. Where the two disagree,
Margo is authoritative and this document is a bug. `[IEO]` marks a rule Margo leaves open; for those
rules this document is authoritative. `[IEO, interim]` marks an `[IEO]` rule that holds only until
a named Appendix B step replaces it.

Relationship to other documents:

- `docs/system-overview.md` explains the design and its reasons. This document is the
  implementation contract. A coding agent SHOULD be able to build a conforming implementation from
  this document alone.
- A change to behavior described here MUST update this document in the same change.
- The reference implementation in this repository uses Go (Gin, ent/Postgres, BoltDB, NATS, Cobra,
  zap). Those choices are not part of conformance.

## 1. Problem Statement

IEO is a three-tier orchestration system. A central orchestrator holds the desired state for every
site. A local orchestrator at each site pulls that state, decides which host runs what, and keeps
the site converged. An agent on each host runs the workloads.

The system solves five operational problems:

- It delivers applications to many sites without an operator touching each site.
- It keeps each site running and converged while the site is disconnected from the center, and
  brings the site up to date in one step when it reconnects.
- It replaces command replay with state convergence: every tier compares desired with actual state
  and acts on the difference, so retries and recovery need no special code paths.
- It interoperates at the central-to-site boundary with other Margo implementations.
- It gives operators one place to see what runs where and whether it is healthy.

Important boundaries:

- IEO decides and reports *what runs where*. It does not read or alter the applications' own
  data (see `docs/proposals/data-plane.md` for the proposed data plane, which carries workload
  traffic but still does not inspect it).
- The central orchestrator never talks to hosts. Hosts never talk to the central orchestrator.
- Only the local orchestrator writes a host's desired state. The host agent never decides what
  should run.

## 2. Goals and Non-Goals

### 2.1 Goals

Numbered so §17.10 can trace each goal to its tests.

1. Serve the Margo Workload Management API from the central tier and consume it from the site tier.
2. Store every deployment revision immutably under its content digest.
3. Keep a site converged to its last accepted desired state while the central tier is unreachable.
4. Reject stale or tampered desired state (rollback protection and digest verification).
5. Place deployments that do not name a host on an eligible host, deterministically.
6. Apply and remove Compose-based deployments idempotently on hosts.
7. Report status from host to site to center, buffering the latest status per deployment while the
   center is unreachable.
8. Recover every tier after a restart from durable local state plus a fresh sync, with no message
   replay.
9. Expose structured logs and metrics for every tier.

### 2.2 Non-Goals

- Helm or Kubernetes deployment types (later phase).
- Web portal, multi-tenancy, or single sign-on (later phase).
- Automated certificate renewal and revocation (Margo has not specified it yet).
- Opaque-gateway mode, or hosts talking to the central tier directly.
- Secret provisioning for workloads (see §15.4).
- Application package signature verification (later phase).
- Prescribing a placement algorithm beyond the deterministic baseline in §8.6.

## 3. System Overview

### 3.1 Main Components

1. `Central Orchestrator (CO)` — Margo Workload Fleet Manager
   - Imports application packages from an OCI Application Registry.
   - Validates and stores Application Descriptions.
   - Registers sites and issues their credentials.
   - Records device capabilities reported by sites.
   - Creates, updates and deletes deployments; stores each revision under its digest.
   - Maintains one State Manifest per site with a strictly increasing `manifestVersion`.
   - Serves the Margo Workload Management API to LOs.
   - Records deployment status and its history.
   - Serves the operator API used by `edgectl`.

2. `Local Orchestrator (LO)` — Margo WFM Client acting as a see-thru gateway
   - Polls the CO for its site's State Manifest.
   - Verifies and durably stores desired state.
   - Reports its own capabilities and those of each host.
   - Places autonomous deployments.
   - Reconciles each host toward its desired deployments.
   - Maps host reports onto Margo deployment status and sends them to the CO.
   - Buffers status in a durable outbox while the CO is unreachable.
   - Runs the site NATS server used for LO ↔ EN messages (§11.2).
   - Does not run workloads itself.

3. `Edge Node agent (EN)` — one per host
   - Publishes capabilities, heartbeat and inventory.
   - Executes Apply and Remove commands for whole deployments.
   - Pulls, verifies and safely extracts Compose archives.
   - Runs each component as a Compose project and monitors its containers.
   - Publishes component status changes.
   - Runs the host's OpenTelemetry collector.

4. `edgectl` — operator CLI
   - Calls the CO operator API. Holds no persistent state.

5. `Application Registry` (external)
   - OCI registry holding application packages and Compose archives.

### 3.2 Abstraction Levels

IEO is easiest to build and port when kept in these layers:

1. `Contract Layer`
   - Margo API types, the site message types (§11.2), and their schema validation.
   - Generated from, or tested against, the pinned Margo OpenAPI and the JSON schemas in this
     document.

2. `Persistence Layer`
   - CO relational store; LO and EN embedded key-value stores.
   - All writes that change desired state are atomic.

3. `Coordination Layer`
   - CO manifest publication; LO sync loop, placement and reconciliation; EN command handling.

4. `Execution Layer`
   - EN archive fetch, validation, parameter injection, and container runtime calls.

5. `Integration Layer`
   - OCI registry client, container runtime (Docker or Podman with Compose), message bus client.

6. `Observability Layer`
   - Structured logs, metrics, and the host OpenTelemetry collector.

### 3.3 External Dependencies

- An OCI Distribution–compatible registry reachable from the CO and from ENs.
- A relational database for the CO.
- A NATS server at each site, operated by the LO.
- Docker or Podman with Compose on each host.
- An X.509 certificate authority for CO ↔ LO mutual TLS, managed with the CO deployment.
- An OpenTelemetry collector binary on each host.

### 3.4 Topology

```text
  Operator ──edgectl──▶ CO ◀──── OCI registry (CO imports packages)
                        ▲
                        │ Margo WM API, HTTPS, mutual TLS, LO polls
          ┌─────────────┼─────────────┐
          LO (site A)   LO (site B)   LO (site N)     one per site, non-hosting
          │ NATS CONTROL account
     ┌────┼────┐
     EN   EN   EN                                     one per host; pull archives from registry
```

## 4. Core Domain Model

### 4.1 Entities

#### 4.1.1 Site

A physical location served by exactly one LO.

Fields:

- `site_id` (string)
  - REQUIRED. Also the LO's Margo device ID.
  - MUST use only RFC 3986 unreserved characters (`A–Z a–z 0–9 . _ ~ -`).
  - The value `any` is reserved and MUST be rejected.
- `state` (enum: `active`, `retired`)
- `client_identity` (string) — the SPIFFE ID of the LO's certificate (§4.2).
- `created_at` (timestamp)

#### 4.1.2 Host

A machine at a site that runs workloads. Runs exactly one EN.

Fields:

- `host_id` (string)
  - REQUIRED. Unreserved characters only. Stable for the life of the host: the EN generates it on
    first start (unless configured) and persists it.
- `device_id` (string) — `<site_id>/<host_id>`. The Margo device ID of the host.
- `capabilities` (DeviceCapabilities, §4.1.3)
- `labels` (map string → string)
- `liveness` (enum, §7.3)
- `last_heartbeat_at` (timestamp or null)

#### 4.1.3 DeviceCapabilities

The Margo `DeviceCapabilitiesManifest` properties for one device `[Margo]`.

- For the LO (non-hosting gateway): identity fields only — `id`, `vendor`, `modelNumber`,
  `serialNumber`. Hosting fields MUST be omitted.
- For a host: identity fields plus `cpus`, `memory`, `storage`, `peripherals`, `interfaces`,
  `otelCollector: true`, `supportedRuntimes: [oci]`, `supportedDeploymentTypes: [compose]`.
- `[IEO]` Hosts additionally carry `labels`, used by eligibility rules.

#### 4.1.4 Application Description

An application's package descriptor (`margo.yaml`) as stored in the registry `[Margo]`.

Fields used by IEO:

- `id` (string) — application ID: lowercase letters, digits and `-`, at most 200 characters.
- `metadata.version` (string) — the version; equal to the registry tag of its package (§5.1).
- `deploymentProfiles` (list) — IEO phase 1 uses profiles with `type: compose` only. Each has:
  - `id` (string)
  - `components` (list), each with `name` and `properties.repository` (`oci://…`),
    `properties.revision` (SemVer, build metadata after `_`; §4.2), and OPTIONAL `wait` (default `true`) and `timeout`.
  - `deviceConstraints` (object, OPTIONAL) — `capacityRequirements` and `eligibilityRules`.
  - vendor extensions `x-…-extensions` (opaque).
- `parameters` (map) — parameter name → `targets` (list of `{pointer, components}`).

The CO MUST preserve vendor extensions exactly and MUST NOT interpret unknown ones.

#### 4.1.5 Application Deployment

One application instance assigned to one target `[Margo]`.

Fields:

- `id` (UUID) — REQUIRED. Assigned at creation and never changed. Edits keep the ID.
- `metadata.name`, `metadata.namespace` (strings) — chosen by the CO: taken from the request,
  each defaulting to the application ID.
- `metadata.deviceId` (string) — the target:
  - Directed: `<site_id>/<host_id>`
  - Autonomous: `<site_id>/*`
- `spec.applicationId` (string)
- `spec.deploymentProfile` — the selected Compose profile: `type`, `components` copied from the
  Application Description, `deviceConstraints` copied **unmodified**.
- `spec.parameters` (map) — parameter name → `{value, targets}`, one entry per parameter of the
  Application Description: `value` is the request's value, or else the description's `value`;
  `targets` are the description's. Always present (`{}` when there are none).
- vendor `x-…-extensions` copied exactly from the Application Description: the description's
  top-level extensions into `spec`, the profile's into `spec.deploymentProfile`, and each
  component's with its component.

Layout `[Margo]`: one YAML document with keys in this order — `id`; `metadata` (`name`,
`namespace`, `deviceId`); `spec` (`applicationId`; `deploymentProfile` (`type`, `components`,
`deviceConstraints` when the profile has them, then the profile's extensions); `parameters`; then the
top-level extensions). The profile's `id` is not copied; Margo's `DesiredState` has no such field.

"Copied exactly" and "copied unmodified" mean the same keys in the same order with the same values.
The CO writes the YAML itself, so the description's indentation, quoting and comments are not kept.

Derived:

- `digest` (string) — `sha256:<hex>` of the serialized YAML bytes. Every edit produces new bytes
  and a new digest. The bytes for a digest never change.

#### 4.1.6 State Manifest

The complete set of deployments assigned to one site `[Margo]`.

Fields:

- `manifestVersion` (integer) — starts at `1` per site; strictly increases on every change to that
  site's deployment set (create, update, delete).
- `deployments` (list) — per deployment: `deploymentId`, `digest`, content URL.
- `bundle` (object or null) — digest and URL of a gzip tar of all deployment YAMLs; `null` when
  there are no deployments. The archive layout is in ADR 0012.

`deployments` is ordered by `deploymentId`, ascending. Adding a site publishes its first manifest:
`manifestVersion` 1, no deployments, `bundle: null`.

A deployment absent from the manifest is to be removed from the site.

#### 4.1.7 Placement `[IEO]`

The LO's durable record of which host runs an autonomous deployment.

- `deployment_id` (UUID)
- `host_id` (string)
- `decided_at` (timestamp)

#### 4.1.8 Host Actual State

What the EN last reported running, taken only from Inventory and status events.

- `host_id`
- `deployments` (map `deployment_id` → `{digest, components: map name → ComponentState}`)
- `reported_at` (timestamp)

#### 4.1.9 Component Status and Deployment Status

- `ComponentState` (enum): `pending`, `installing`, `installed`, `removing`, `removed`, `failed`.
- `ComponentStatus`: `{name, state, error?}` where `error` is `{code, source, message}`.
- `DeploymentStatus` `[Margo]`: `{deploymentId, deviceId, adoptedManifestVersion, status: {state},
  components: [ComponentStatus]}` with exactly one entry per component in the deployment.

#### 4.1.10 Outbox Entry `[IEO]`

The latest not-yet-acknowledged DeploymentStatus for one deployment, held durably by the LO.

- Keyed by `deployment_id`. A newer entry replaces an older one.

#### 4.1.11 LO Runtime State

Single authoritative state owned by the LO. Durable fields MUST survive restart.

- `accepted_manifest_version` (integer or null) — durable
- `etag` (string or null) — durable
- `desired` (map `deployment_id` → `{digest, yaml, adopted_manifest_version}`) — durable
- `placements` (map `deployment_id` → Placement) — durable
- `hosts` (map `host_id` → Host) — durable (capabilities, labels, `last_heartbeat_at`); liveness
  is derived from `last_heartbeat_at`
- `actual` (map `host_id` → Host Actual State) — durable; replaced by each inventory
- `retry` (map `(host_id, deployment_id)` → `{attempt, due_at, last_error}`) — in memory
- `outbox` (map `deployment_id` → Outbox Entry) — durable

#### 4.1.12 EN Runtime State

- `host_id` — durable
- `applied` (map `deployment_id` → `{digest, compose_projects: [string]}`) — durable
- `component_states` (map `(deployment_id, component)` → ComponentStatus) — durable
- `in_flight` (map `deployment_id` → command) — in memory

### 4.2 Stable Identifiers and Normalization Rules

- `Deployment ID`
  - Opaque UUID. The only key for a deployment in every tier and every message.
- `Digest`
  - Always written `sha256:<lowercase hex>`. Compare as exact strings.
- `Device ID`
  - LO: `<site_id>`. Host: `<site_id>/<host_id>`. Autonomous target: `<site_id>/*`.
  - Split on the first `/` only.
- `Compose Project Name` `[IEO]`
  - `<deployment_id>-<component_name>`, lowercased; characters outside `[a-z0-9_-]` replaced with
    `-`.
- `Component Revision` `[Margo]`
  - `revision` is the OCI tag. OCI tags cannot contain `+`, so SemVer build metadata is written
    after `_` (`1.2.3_build.5`). A tag and a revision compare as exact strings.
  - To read a revision as SemVer (precedence, display), convert `_` to `+`.
- `SPIFFE IDs` `[Margo]`
  - CO: `spiffe://<trust-domain>/margo/wfm/<wfm-id>`
  - LO: `spiffe://<trust-domain>/margo/wfm/<wfm-id>/client/<site_id>`
- `Message subjects` `[IEO]`
  - `site.<site_id>.host.<host_id>.<kind>` (§11.2).

## 5. Application Package Contract

### 5.1 Registry Layout `[Margo]`

- One OCI repository per application; one tag per `metadata.version`, equal to it.
- The package is an OCI image manifest with `artifactType` `application/vnd.margo.app.v1+json` and
  an empty config. Each file of the package is one layer: exactly one layer of media type
  `application/vnd.margo.app.description.v1+yaml` holds the Application Description; the others
  are its resources (icon, release notes, description and license files).
- Each Compose component's `repository` points to a separate OCI artifact holding a **Margo
  Compose Archive**:
  - manifest `artifactType`: `application/vnd.org.margo.component.compose+json`
  - exactly one layer of media type `application/vnd.org.margo.component.compose.tar+gzip`

### 5.2 Compose Archive Rules `[Margo]`

An archive is valid only if all hold:

- It has exactly one top-level directory.
- That directory contains a file named `compose.yaml` (no other file name is accepted).
- No entry has an absolute path or a `..` segment.
- No symbolic or hard link points outside the top-level directory.

On extraction the EN MUST strip setuid, setgid and sticky bits. Any violation fails the component.

### 5.3 Import Validation (CO)

The CO imports one version at a time: the tag of that version in the application's repository.
It MUST reject the import, with the reason, and store nothing, when:

- the tag's manifest is not a Margo application package (§5.1);
- the Application Description is not exactly one YAML document, or uses YAML aliases (they can
  expand without bound; §15.1);
- the Application Description fails schema validation against the pinned Margo schema (header);
- its `metadata.version` differs from the tag;
- it has no deployment profile of a supported type (`compose` in phase 1);
- it has a component whose `repository` is not an `oci://` reference or whose `revision` is not
  SemVer in the §4.2 form;
- it has a parameter target naming a component that is in none of its deployment profiles;
- it duplicates an already imported `(id, metadata.version)` with different content.

Content is the exact bytes of the Application Description. Re-importing an identical version MUST
succeed without changes.

### 5.4 Parameter Semantics for Compose `[IEO]`

- Each parameter target `pointer` is the **name of an environment variable**.
- The value is set for each component listed in the target.
- The EN writes values into the Compose project's environment file for that component.
- Secrets MUST NOT be read from the archive. Secret provisioning is out of scope (§15.4).

### 5.5 Device Constraint Evaluation `[IEO]`

A host *satisfies* a profile's `deviceConstraints` when it satisfies every `eligibilityRule` and,
where §8 asks for it, the `capacityRequirements`. Margo defines the operators; IEO fixes what Margo
leaves open. The CO (§8.1.1) and the LO (§8.4, §8.6) use the same rules.

Eligibility rules:

- Every rule MUST match. Within a rule, `propertySelector` and `labelSelector` MUST both match
  (an absent selector matches). Within a selector, every match expression MUST match `[Margo]`.
- A property selector's `key` is an RFC 6901 JSON Pointer into the host's capabilities
  `properties` object as encoded on the wire (§11.1), e.g. `/cpus/0/architecture`. A label
  selector's `key` is a label name.
- A key is absent when the pointer resolves to nothing or the label is not set. For an absent key,
  `NotIn` and `DoesNotExist` match; `In`, `Gt`, `Lt`, `Exists`, `ContainsAll` and `ContainsAny`
  do not.
- `In` / `NotIn`: the value equals (does not equal) one of `values`. Property values compare by
  JSON type and value (numbers numerically). Labels are strings (§4.1.2) and compare by text form:
  label `"2"` equals `2` and `"2"`, label `"true"` equals `true`.
- `Gt` / `Lt`: `values` holds exactly one number, and the value is a number (for a label, its text
  read as a decimal number) greater (less) than it. Anything else does not match.
- `ContainsAll` / `ContainsAny`: the value is an array, and some element satisfies all (at least
  one) of the `itemSelector` expressions, whose keys are JSON Pointers relative to the element.

Capacity requirements, against the host's reported capabilities:

- `cpu`: a single `cpus` entry has at least `cores` and, when `architectures` is given, an
  `architecture` in it. Cores from different entries are never added `[Margo]`.
- `memory`, `storage`: the reported amount, in binary units (`Ki`…`Ei`), is at least the required
  amount.
- A host that does not report a required field does not satisfy the requirement.

## 6. Configuration Specification

### 6.1 Configuration Resolution

Each tier reads its own configuration. The file format and file location are
implementation-defined; environment variables MUST be able to override any key.

Resolution order (later wins):

1. Built-in defaults (§6.3).
2. Configuration file.
3. Environment variables.
4. Command-line flags.

A tier MUST validate its configuration at startup and exit non-zero with an operator-readable error
if validation fails.

### 6.2 Dynamic Reload

Dynamic reload is OPTIONAL. If implemented, a failed reload MUST keep the last valid configuration
and emit an operator-visible error. Identity settings (site ID, host ID, certificates) MUST NOT
change without a restart.

### 6.3 Core Config Fields Summary (Cheat Sheet)

This section is intentionally redundant so a coding agent can implement the config layer quickly.

CO:

- `co.listen_addr`: string, default `:443` (Margo API)
- `co.operator_listen_addr`: string, default `:8443` (operator API)
- `co.database_url`: string, REQUIRED
- `co.wfm_id`: string, REQUIRED
- `co.trust_domain`: string, REQUIRED
- `co.tls.cert_file`, `co.tls.key_file`, `co.tls.client_ca_file`: paths, REQUIRED from Appendix B
  step 4; not read before it (§15.6)
- `co.ca.cert_file`, `co.ca.key_file`: paths, REQUIRED (issues LO certificates)
- `co.operator_token`: string, REQUIRED in phase 1
- `co.registry.insecure`: boolean, default `false`
- `co.metrics_listen_addr`: string, default `:9090`

LO:

- `lo.site_id`: string, REQUIRED
- `lo.co_url`: string, REQUIRED
- `lo.tls.cert_file`, `lo.tls.key_file`, `lo.tls.ca_file`: paths, REQUIRED from Appendix B step 4.
  Before it, only `lo.tls.ca_file` is read, OPTIONAL: it verifies the CO's certificate (§15.6)
- `lo.co_insecure`: boolean, default `false` `[IEO, interim]`: allows an `http://` `lo.co_url`
  (§15.6)
- `lo.data_dir`: path, REQUIRED
- `lo.poll.interval`: duration, default `60s` `[Margo: user-configurable]`
- `lo.poll.hours`: list of hour ranges, default all hours `[Margo]`
- `lo.poll.downtime_windows`: list of time windows, default `[]` `[Margo]`
- `lo.poll.max_backoff`: duration, default `10m`
- `lo.nats.listen_addr`: string, default `:4222`
- `lo.nats.tls.cert_file`, `lo.nats.tls.key_file`: paths, REQUIRED once the LO runs the site NATS
  server (§11.2); not read before it
- `lo.nats.operator_key_file`: path, REQUIRED from Appendix B step 4 (scoped NATS credentials); not
  read before it. It signs per-host and, with the data-plane proposal
  `docs/proposals/data-plane.md`, per-workload credentials
- `lo.heartbeat.interval`: duration, default `10s` (MUST equal the ENs' `en.heartbeat.interval`)
- `lo.heartbeat.offline_after_missed`: integer, default `3`
- `lo.metrics_listen_addr`: string, default `:9091`
- `lo.reconcile.interval`: duration, default `5m` (safety net)
- `lo.reconcile.retry_base`: duration, default `10s`
- `lo.reconcile.retry_max`: duration, default `5m`
- `lo.command.ack_timeout`: duration, default `5s`
- `lo.placement.enabled`: boolean, default `true`

EN:

- `en.site_id`: string, REQUIRED
- `en.host_id`: string, OPTIONAL (generated and persisted on first start if absent)
- `en.nats_url`: string, REQUIRED
- `en.nats.creds_file`, `en.nats.ca_file`: paths, REQUIRED
- `en.data_dir`: path, REQUIRED
- `en.runtime`: `docker` or `podman`, default implementation-defined
- `en.labels`: map string → string, default `{}`
- `en.heartbeat.interval`: duration, default `10s`
- `en.inventory.interval`: duration, default `60s`
- `en.otel.enabled`: boolean, default `true`
- `en.registry.auth_file`: path, OPTIONAL (credentials for pulling Compose archives)
- `en.registry.insecure`: boolean, default `false`
- `en.metrics_listen_addr`: string, default `:9092`

edgectl:

- `edgectl.co_url`: string, REQUIRED
- `edgectl.token`: string, REQUIRED in phase 1

## 7. State Machines

Each tier has one authority for its mutable state. Workers and message handlers report outcomes to
that authority, which turns them into explicit transitions.

### 7.1 Component State (EN)

| From | Event | To |
| --- | --- | --- |
| (none) / `pending` | Apply received | `installing` |
| `installing` | all containers running (`wait: true`) or Compose succeeded (`wait: false`) | `installed` |
| `installing` | pull, digest, archive or start-timeout error | `failed` |
| `installed` | container exits and does not recover under its restart policy | `failed` |
| `failed` | container recovers | `installed` |
| `failed` | Apply received (retry) | `installing` |
| `installed` | Apply of a different digest | `installing` |
| any | Remove received | `removing` |
| `removing` | all Compose projects down | `removed` |

Rules:

- `pending`: the deployment is desired for this host but the EN has not started applying it. The LO
  reports `pending` for hosts that are offline.
- `removed` is terminal for that deployment on that host.

### 7.2 Deployment Status Aggregation `[Margo]`

`status.state` is the most severe component state, using this precedence:

```text
failed > removing > installing > pending > installed > removed
```

### 7.3 Host Liveness (LO)

1. `Unknown` — host known from durable store, no heartbeat since LO start.
2. `Online` — heartbeat received within `lo.heartbeat.offline_after_missed × lo.heartbeat.interval`.
3. `Offline` — no heartbeat within that window.
4. `Decommissioned` — removed by operator or by configuration; terminal.

Transitions:

- `Unknown|Offline` → `Online`: first heartbeat or inventory. The LO MUST reconcile the host.
- `Online` → `Offline`: missed-heartbeat window elapses. The LO MUST stop sending commands and
  report the host's deployments as `pending`.
- any → `Decommissioned`: the LO sends `DELETE` capabilities for the host and re-places its
  autonomous deployments (§8.6).

### 7.4 Sync Attempt Outcomes (LO)

Each sync attempt (§8.2) ends in exactly one outcome. Distinct outcomes matter because logging,
backoff and security handling differ.

1. `NotModified` — `304`.
2. `Accepted` — new manifest verified and committed.
3. `RejectedRollback` — `manifestVersion` not greater than stored. Security event.
4. `AbortedDigestMismatch` — any fetched artifact failed digest verification, or a bundle does not
   match the manifest (§8.2). Security event.
5. `Unreachable` — transport error, `5xx` without `Retry-After`, a manifest that fails validation,
   or a content fetch that failed (§8.2). Backoff.
6. `Throttled` — `429`, or any response carrying `Retry-After`. Wait as instructed.
7. `Retired` — `403` with problem type `#not-authorized` on `GET /api/v1/deployments`: the CO
   refuses this client by local policy, of which retirement is the case Margo names (§11.1). Margo
   clients key on the type, never the title. Stop polling.
8. `SkippedWindow` — outside polling hours or inside a planned downtime window.

### 7.5 Transition Triggers (LO)

- `Poll Timer Fired` → sync attempt (§8.2).
- `Manifest Accepted` → reconcile all hosts.
- `Heartbeat` → update liveness; if the host was not `Online`, reconcile it.
- `Inventory` → replace the host's actual state; reconcile the host.
- `Component Status Event` → update actual state; enqueue DeploymentStatus in the outbox; if a
  component became `failed`, schedule a retry for that `(host, deployment)`; reconcile the host.
- `Command Rejected or Ack Timeout` → schedule per-deployment retry (§8.5).
- `Retry Timer Fired` → reconcile that host.
- `Reconcile Safety Timer Fired` → reconcile all hosts.
- `Heartbeat Window Elapsed` → host `Offline`.
- `CO Reachable Again` → flush outbox (§8.8).

### 7.6 Idempotency and Recovery Rules

- The LO serializes all mutations of its runtime state through one authority (one goroutine, actor,
  or lock). No two reconcile passes for the same host run concurrently.
- Actual state changes **only** on EN reports. Sending a command never changes actual state.
- Apply of a digest that is already `installed` MUST succeed with no side effects. Remove of a
  deployment that is not present MUST succeed with no side effects.
- The EN never changes what should run. It only executes commands and reports.
- Missed messages are recovered by state, never by replay: an EN publishes full inventory on every
  (re)connect, and the LO reconciles against it.

## 8. Synchronization, Placement, and Reconciliation

### 8.1 CO: Deployment Creation and Manifest Publication

On create or update of a deployment the CO MUST, in one transaction:

1. Build the ApplicationDeployment (§4.1.5) from the Application Description, the selected
   profile, the target, and the parameters.
2. Run preliminary checks (§8.1.1). A failing check rejects the request and creates nothing.
3. Serialize the YAML once and compute its digest.
4. Store the bytes immutably under the digest.
5. Point the deployment at the new digest.
6. Rebuild the site's State Manifest and increment its `manifestVersion`.

On delete the CO removes the deployment from the site's manifest and increments
`manifestVersion`. Stored YAML for old digests MAY be kept for history.

`manifestVersion` increments only when the site's set of `(deploymentId, digest)` changes. An update
that rebuilds identical bytes, or a delete of a deployment already deleted, changes no manifest. An
update keeps the deployment's site: a target at another site is rejected (delete and create
instead).

#### 8.1.1 Preliminary Checks

- The target site exists and is `active`.
- Directed: the host exists and its last reported capabilities satisfy `eligibilityRules` and
  `capacityRequirements` (§5.5).
- Autonomous: at least one host at the site satisfies them.
- Every REQUIRED parameter has a value. A parameter is REQUIRED when its Application Description
  gives it no `value`, since Margo requires a `value` for every deployment parameter. A request
  naming a parameter the description does not define is rejected.

#### 8.1.2 Status Ingestion

- The CO appends every received DeploymentStatus to the deployment's status history and updates its
  current status.
- When a deleted deployment is reported `removed` on its device, with an `adoptedManifestVersion`
  at least that of the manifest that deleted it, the CO marks the deployment removed.
- A status whose `adoptedManifestVersion` is older than the version holding the deployment's current
  digest is recorded in history but does not replace the current status, and does not mark a
  deleted deployment removed. The version holding the current digest is the first manifest that
  carried it; an update that rebuilds the same digest keeps it.

#### 8.1.3 Manifest Serialization `[Margo]`

- The manifest body MUST be serialized deterministically using RFC 8785 (JSON Canonicalization
  Scheme), so identical content yields identical bytes.
- `ETag` is the quoted `sha256:<hex>` of the response bytes.
- Media type: `application/vnd.margo.manifest.v1+json`.

### 8.2 LO: Sync Loop

At the configured interval, subject to polling hours and downtime windows:

1. `GET /api/v1/deployments`, with `If-None-Match: <etag>` when an ETag is stored.
2. `304` → outcome `NotModified`.
3. `200` → validate the manifest `[IEO]`: it validates against the pinned Margo schema, names each
   `deploymentId` at most once, and has a non-null `bundle` when it has deployments (§4.1.6).
   Otherwise outcome `Unreachable`, logged at error level with the reason; keep previous desired
   state. Then check `manifestVersion > accepted_manifest_version`. Otherwise outcome
   `RejectedRollback`; keep previous desired state.
4. Determine digests not already held. Fetch them:
   - first sync, or more than half the deployments changed → `GET /api/v1/bundles/{digest}`;
   - otherwise → `GET /api/v1/deployments/{deploymentId}/{digest}` for each.
   The threshold is implementation-defined; the RECOMMENDED value is half. `[IEO]` The LO
   fetches the bundle when the deployments whose digest it does not hold are more than half of the
   deployments in the manifest; a first sync holds none. The threshold is not configurable.
5. Verify the SHA-256 of every fetched artifact against its digest. A bundle's bytes are verified
   against the manifest's bundle digest before the bundle is unpacked. A fetched bundle MUST also
   match the manifest: exactly one entry per deployment in the manifest and nothing else, laid out
   as in ADR 0012, each entry matching that deployment's digest. Any mismatch → outcome
   `AbortedDigestMismatch`; keep previous desired state; the next poll retries.
6. Atomically replace `desired` with the new set. Record for each deployment the
   `manifestVersion` its current digest first appeared in (`adopted_manifest_version`).
7. Trigger reconciliation of all hosts.
8. Once reconciliation has been started for every deployment, durably persist the new
   `manifestVersion` and ETag. Outcome `Accepted`.

Rules:

- `[IEO]` A content fetch that fails ends the sync attempt and leaves desired state unchanged: outcome
  `Throttled` for a `429` or a response carrying `Retry-After`, `Unreachable` for anything else.
  The next poll retries the fetch, subject to `retryable` and `backoffStrategy` (below); a failed
  content fetch never stops polling the manifest.
- A `404` on a content URL means only that the digest is unavailable. It is never a removal
  signal.
- A deployment absent from an accepted manifest is removed.
- After an outage, the first successful poll returns the full current manifest. No intermediate
  manifest is needed.
- The LO MUST honor `Retry-After` and the problem fields `retryable` and `backoffStrategy`
  `[Margo]`. Without them, it uses exponential backoff capped at `lo.poll.max_backoff`.

### 8.3 LO: Capability Reporting

1. On start, the LO sends `PUT /api/v1/capabilities/<site_id>` with its own identity fields.
2. Only after that succeeds, for each known host, it sends
   `PUT /api/v1/capabilities/<site_id>/<host_id>`. The CO rejects host reports for an unknown
   gateway with `404 gateway-not-found` `[Margo]`.
3. The LO re-sends a host's capabilities when the EN publishes a change.
4. On decommission the LO sends `DELETE /api/v1/capabilities/<site_id>/<host_id>`.

Capability reports that fail because the CO is unreachable are retried with backoff. Only the
latest report per device is kept.

### 8.4 LO: Target Resolution

For each desired deployment:

- Directed (`<site_id>/<host_id>`):
  - Unknown host → status `failed`, error code `101` (unknown child device), source = LO device ID.
  - Host fails `eligibilityRules` → status `failed`.
  - The LO SHOULD check `capacityRequirements`; failure → status `failed`.
- Autonomous (`<site_id>/*`): use the stored Placement if it exists and its host is not
  decommissioned and still satisfies the constraints; otherwise place (§8.6).
- If `lo.placement.enabled` is false, autonomous deployments get status `failed` with error code
  `103` `[Margo]`.

### 8.5 LO: Host Reconciliation

For one host that is `Online`:

1. `want` = desired deployments resolved to this host (with digests).
2. `have` = actual state for this host.
3. For each deployment in `want` not in `have`, in `have` with a different digest, or in `have` at the
   same digest with any component `failed` → **Apply**.
4. For each deployment in `have` not in `want` → **Remove**.
5. Otherwise, nothing.
6. Skip any `(host, deployment)` whose retry entry is not yet due.

A retry for that `(host, deployment)` is scheduled when the command's ack is `accepted: false`, when
it is not acknowledged within `lo.command.ack_timeout`, or when the host later reports a component
of it `failed`. Retries use exponential backoff from `lo.reconcile.retry_base` up to
`lo.reconcile.retry_max`. A retry entry is cleared when the
host reports the desired digest `installed` or the deployment is no longer wanted.

Operations are whole-deployment. Converging the components inside a deployment is the EN's job.

### 8.6 LO: Autonomous Placement `[IEO]`

Among hosts that are `Online`, satisfy `eligibilityRules`, and have enough unreserved capacity for
`capacityRequirements`:

1. Pick the host with the most free memory, where free memory = reported memory − sum of
   `capacityRequirements.memory` of deployments already placed or directed there.
2. Break ties by `host_id`, lexicographic ascending.
3. Record the Placement durably before sending Apply.

If no host qualifies: status `failed`, error code `IEO-NO-ELIGIBLE-HOST`, retried on the next
reconcile pass.

A Placement is kept for the life of the deployment unless its host is decommissioned or stops
satisfying the constraints. Placement MUST NOT move a deployment only because another host has
become a better fit.

### 8.7 LO: Status Mapping

When an EN component status event or inventory changes a deployment's state on a host, the LO
builds a DeploymentStatus:

- `deploymentId`
- `deviceId` = `<site_id>/<host_id>` of the host (for autonomous deployments, the placed host).
- `adoptedManifestVersion` = the `adopted_manifest_version` recorded for this deployment's current
  digest (§8.2 step 6), not simply the latest manifest fetched.
- `components`: exactly one entry per component in the deployment YAML. A component with no report
  yet is `pending`.
- `status.state` by §7.2.
- Error `source`: the component name for component errors; `metadata.name` for deployment-level
  errors; the LO's device ID for gateway errors (`101`, `102`, `103`).

The LO writes the DeploymentStatus to the outbox, then tries to send it.

### 8.8 LO: Status Outbox `[IEO]`

- `POST /api/v1/deployments/{deploymentId}/status` for each outbox entry.
- On `2xx` the entry is removed only if it has not been replaced since it was sent.
- On failure the entry stays. The LO retries with the same backoff rules as the sync loop.
- The outbox holds at most one entry per deployment; a newer status replaces an older one.
- On reconnect after an outage, the LO flushes the outbox before or concurrently with the next sync.

### 8.9 EN: Command Handling

On **Apply** (deployment ID, digest, deployment YAML):

1. If `applied[deployment_id].digest == digest` and all components are `installed`, ack
   `accepted: true` and do nothing else.
2. Validate the deployment YAML (schema, profile type `compose`). Invalid → ack `accepted: false`
   with error.
3. Ack `accepted: true`. The rest runs asynchronously; outcomes are reported as status events.
4. For each component, in listed order:
   1. Publish `installing`.
   2. Pull the Compose Archive from `repository` at tag `revision` (§4.2 tag rule). Verify the layer
      digest against the OCI manifest before extracting.
   3. Validate and extract the archive (§5.2) into
      `<en.data_dir>/deployments/<deployment_id>/<digest>/<component>/`.
   4. Write the environment file: parameter values for this component (§5.4) plus the OpenTelemetry
      collector variables required by Margo.
   5. Bring up the Compose project `<deployment_id>-<component>`. If `wait` is true, wait until all
      containers are running or `timeout` elapses.
   6. Publish `installed`, or `failed` with an error and stop processing later components.
5. If the deployment previously ran at a different digest, bring down Compose projects of
   components that no longer exist.
6. Durably record `applied[deployment_id] = {digest, compose_projects}` whether processing succeeded
   or failed, so inventory reports the digest with its component states and the LO can retry.

On **Remove** (deployment ID):

1. If the deployment is not in `applied`, ack `accepted: true`, publish `removed`, and stop.
2. Ack, publish `removing`, bring down all its Compose projects, delete its working directory,
   remove it from `applied`, publish `removed`.

Concurrency: the EN MUST process at most one command per deployment at a time. A new command for a
deployment with a command in flight supersedes it after the in-flight step finishes.

### 8.10 EN: Monitoring

- The EN watches container events for every applied deployment.
- A container that exits unexpectedly and does not recover under its Compose restart policy moves
  its component to `failed` with error code `IEO-CONTAINER-EXITED` and the container's exit
  information in the message.
- A container that later recovers moves the component back to `installed`.

## 9. Host Execution Environment and Safety

### 9.1 Directory Layout (EN)

```text
<en.data_dir>/
  host.id                              persisted host ID
  state.db                             embedded store (applied, component_states)
  deployments/<deployment_id>/<digest>/<component>/
      <top-level-dir>/compose.yaml     extracted archive
      .env                             parameters + injected variables
  otel/config.yaml                     collector configuration
```

### 9.2 Safety Invariants

- Every extracted path MUST resolve inside the component's directory. Implementations MUST check the
  resolved path of every archive entry before writing it.
- Archive extraction MUST NOT follow links while writing.
- setuid, setgid and sticky bits MUST be cleared on every extracted file.
- The EN MUST NOT read secrets from the archive.
- The EN MUST NOT pass its NATS CONTROL credentials into any workload.
- Compose project names MUST be derived only from the deployment ID and component name (§4.2),
  never from archive content.

### 9.3 OpenTelemetry Collector

- The EN runs a collector with host-metrics and container-metrics receivers and an OTLP receiver.
- No exporters are pre-configured. Operators add exporters.
- The EN injects the collector connection variables required by Margo into every container.

## 10. Status Reporting Contract

This section collects the reporting rules in one place.

- Component status flows EN → LO as `ComponentStatusEvent`; full state as `Inventory`.
- The LO is the only sender of Margo DeploymentStatus.
- Every DeploymentStatus MUST contain exactly one component entry per component (§8.7).
- States and aggregation follow §7.1 and §7.2.
- Error codes:
  - `101` unknown child device ID `[Margo]`
  - `102` gateway-generated error defined by Margo; see the Margo specification `[Margo]`
  - `103` autonomous placement not supported `[Margo]`
  - `IEO-NO-ELIGIBLE-HOST` no host satisfies constraints `[IEO]`
  - `IEO-ARCHIVE-INVALID` archive failed §5.2 `[IEO]`
  - `IEO-DIGEST-MISMATCH` pulled layer digest did not match `[IEO]`
  - `IEO-PULL-FAILED` registry unreachable or artifact missing `[IEO]`
  - `IEO-START-TIMEOUT` containers not running within `timeout` `[IEO]`
  - `IEO-COMPOSE-FAILED` the Compose command failed for another reason `[IEO]`
  - `IEO-CONTAINER-EXITED` container exited and did not recover `[IEO]`

## 11. Interfaces

### 11.1 CO ↔ LO: Margo Workload Management API `[Margo]`

Server: CO. Client: LO. HTTPS over HTTP/1.1, port 443, mutual TLS (TLS 1.3 by default).

| Endpoint | Direction | Purpose |
| --- | --- | --- |
| `PUT /api/v1/capabilities/{deviceId}` | LO → CO | Report capabilities of the LO or a host |
| `DELETE /api/v1/capabilities/{deviceId}` | LO → CO | Unregister a host |
| `GET /api/v1/deployments` | CO → LO | State Manifest, with ETag and `304` |
| `GET /api/v1/deployments/{deploymentId}/{digest}` | CO → LO | One deployment YAML |
| `GET /api/v1/bundles/{digest}` | CO → LO | All deployment YAMLs as gzip tar |
| `POST /api/v1/deployments/{deploymentId}/status` | LO → CO | Deployment status |

CO obligations:

- Identify the caller **only** from its client certificate's SPIFFE ID, never from the path or
  body. Expose only that caller's resources.
- Return `403` to a retired site, with problem type
  `https://docs.margo.org/specification/problem-types#not-authorized` and title `Client
  Relationship Retired`. Margo's prose names this type `wfm-client-relationship-retired`, but its
  problem-type registry, which responses MUST use, has only `#not-authorized` (see
  `docs/margo-pins.md`). The site is checked again when a report is written: a site retired
  while its request was in flight gets the same `403`, and a site that no longer exists gets
  `401`, as an unknown token does (§15.6).
- Serve a deployment YAML only if its digest is one that deployment had in a manifest of the
  caller's site, and a bundle only if a manifest of the caller's site named it. Everything else,
  including another site's content and a malformed ID or digest, gets `404` with problem type
  `#deployment-not-found` or `#invalid-bundle`, so a caller can't learn what other sites hold.
  Earlier digests stay available, so an LO that fetched an older manifest can finish its sync.
- Answer `If-None-Match` matching the current ETag (weak comparison, `*` included) with `304` on
  `GET /api/v1/deployments` and `GET /api/v1/bundles/{digest}`.
- Answer `GET /api/v1/deployments` with `406` and problem type `#server-cannot-generate-response`
  when `Accept` admits neither `application/vnd.margo.manifest.v1+json` nor a matching wildcard.
- Request bodies: unparseable JSON gets `400` `#invalid-request`. A body that fails the Margo
  schema or a rule below gets `422` `#semantic-error` with `errors[]`.
- Capabilities (`PUT`/`DELETE /api/v1/capabilities/{deviceId}`), with `{deviceId}` `<site_id>` or
  `<site_id>/<host_id>`:
  - A top-level ID other than the caller's site gets `403` `#not-authorized` (Margo local policy).
  - `422`: any other form of `{deviceId}`, `properties.id` different from the path, or a label
    value that is not a string (labels are strings, §5.5).
  - `PUT` answers `201` for a new device and `200` for a replaced report; a host before its
    site's gateway report gets `404` `#gateway-not-found`.
  - `DELETE` answers `204`; an unknown host gets `404` `#device-not-found`, and the gateway itself
    `422` (retire the site instead).
- Status (`POST /api/v1/deployments/{deploymentId}/status`), §8.1.2: `201` for a deployment's first
  report, `200` after. `422` when the body's `deploymentId` differs from the path, the deployment
  is not the caller's, `deviceId` is at another site, or `adoptedManifestVersion` is `0` or above
  the site's current `manifestVersion`. The Margo file lists no `404` for this operation.
- Serve deployment YAML and bundles byte-for-byte identical to their digests, with
  `Cache-Control: private, max-age=31536000, immutable`.
- Serve the manifest with `Cache-Control: private` only.
- Return errors as RFC 9457 `application/problem+json` with Margo problem types; a condition with
  no Margo type uses `about:blank` (Margo problem-type registry).
- Include `bundle: null` when the site has no deployments.

### 11.2 LO ↔ EN: Site Messages `[IEO]`

Transport: core NATS (not JetStream), TLS, per-host credentials, in the site server's `CONTROL`
account. Payloads are JSON. `<s>` = site ID, `<h>` = host ID.

| Subject | Direction | Pattern | Payload |
| --- | --- | --- | --- |
| `site.<s>.host.<h>.cmd` | LO → EN | request/reply | `Command` → `CommandAck` |
| `site.<s>.host.<h>.status` | EN → LO | publish | `ComponentStatusEvent` |
| `site.<s>.host.<h>.inventory` | EN → LO | publish on start, on reconnect, every `en.inventory.interval` | `Inventory` |
| `site.<s>.host.<h>.heartbeat` | EN → LO | publish every `en.heartbeat.interval` | `Heartbeat` |
| `site.<s>.host.<h>.capabilities` | EN → LO | publish on start and on change | `Capabilities` |
| `site.<s>.inventory.request` | LO → all ENs | publish | `{}` — every EN answers by publishing its `Inventory` |

Permissions:

- An EN MAY publish only on `site.<s>.host.<h>.>` for its own `<h>`, and subscribe only to its own
  `.cmd` subject and to `site.<s>.inventory.request`.
- The LO MAY publish and subscribe on all `site.<s>.>` subjects of its site.

Messages:

```jsonc
// Command (LO → EN)
{ "commandId": "uuid", "action": "apply" | "remove",
  "deploymentId": "uuid", "digest": "sha256:…",
  "deployment": { /* ApplicationDeployment; present only for "apply" */ } }

// CommandAck (EN → LO): acceptance only; outcomes arrive as status events
{ "commandId": "uuid", "accepted": true,
  "error": { "code": "…", "message": "…" } }          // present only when accepted is false

// ComponentStatusEvent (EN → LO)
{ "deploymentId": "uuid", "digest": "sha256:…", "component": "name",
  "state": "pending|installing|installed|removing|removed|failed",
  "error": { "code": "…", "source": "…", "message": "…" },   // OPTIONAL
  "at": "RFC 3339" }

// Inventory (EN → LO): the EN's complete actual state
{ "hostId": "host-03", "at": "RFC 3339",
  "deployments": [ { "deploymentId": "uuid", "digest": "sha256:…",
                     "components": [ { "name": "…", "state": "…" } ] } ] }

// Heartbeat (EN → LO)
{ "hostId": "host-03", "at": "RFC 3339", "uptimeSeconds": 12345 }

// Capabilities (EN → LO): Margo DeviceCapabilitiesManifest properties plus labels
{ "hostId": "host-03", "capabilities": { /* §4.1.3 */ }, "labels": { "line": "2" } }
```

The normative schemas for these messages are the JSON Schemas (draft 2020-12) in
`internal/contract/schemas/site/`, one per message. In every message:

- All fields shown above are REQUIRED except where marked OPTIONAL or conditional.
- `commandId` and `deploymentId` are UUIDs; `digest` matches `^sha256:[0-9a-f]{64}$`; `at` is an
  RFC 3339 date-time; `hostId` follows §4.2; `uptimeSeconds` is an integer ≥ 0; `state` is a
  `ComponentState` (§4.1.9).
- `Command.deployment` is REQUIRED when `action` is `apply` and MUST be absent when it is `remove`.
- `CommandAck.error` is REQUIRED when `accepted` is `false` and MUST be absent when it is `true`.
- `Inventory.deployments` and `Capabilities.labels` are present even when empty.

Receivers MUST ignore unknown fields. A message that fails schema validation MUST be logged and
dropped; it MUST NOT change state.

### 11.3 edgectl ↔ CO: Operator API `[IEO]`

JSON over HTTPS. Phase 1 authenticates with a static bearer token (`co.operator_token`).

| Resource | Operations |
| --- | --- |
| `/apps` | import from registry, list, get, delete |
| `/sites` | add (issues LO credentials), list, get, retire |
| `/devices` | list, get (read-only) |
| `/deployments` | create, update, delete, list, get |
| `/deployments/{id}/status` | current status and history |
| `/deployments/{id}/stream` | server-sent events of status changes |

Minimum CLI commands:

```text
edgectl app import --repo <registry>/<namespace>/<app> [--version <v>]
edgectl app list | get <app-id> | delete <app-id>
edgectl site add <site-id>            # writes LO credentials to a local directory
edgectl site list | get <site-id> | retire <site-id>
edgectl device list [--site <site-id>]
edgectl deploy --app <app-id> --version <v> [--profile <profile-id>] \
              --target <site-id>/<host-id>|<site-id>/* [--param k=v]...
              # --profile defaults to the only compose profile; required if there are several
edgectl deployment list | get <id> | update <id> ... | delete <id>
edgectl deployment status <id> [--watch]
```

`edgectl` MUST exit non-zero on any failed request and print the problem `title` and `detail`.

## 12. Persistence

| Tier | Store | Contents | Durability rule |
| --- | --- | --- | --- |
| CO | Relational database | Apps and versions; sites; devices and latest capabilities; deployments (ID, current digest, target, parameters); immutable YAML by digest; per-site manifest and `manifestVersion`; status history | Deployment change + manifest version increment in one transaction |
| LO | Embedded key-value store | `accepted_manifest_version`, `etag`, `desired`, `placements`, hosts, `outbox` | `desired` replaced atomically; version and ETag persisted after reconcile starts (§8.2) |
| EN | Embedded key-value store | `host_id`, `applied`, `component_states` | `applied` updated after each Apply/Remove completes |

Rules:

- The LO MUST persist `manifestVersion` and ETag durably, so rollback protection survives restart.
- If the LO loses its store, it re-syncs from the CO (full manifest) and from ENs (inventories).
  The version it then reports is the one it re-synced against.
- `manifestVersion` at the CO MUST never decrease, including across database restore. A restore
  procedure MUST set each site's version above its last published value.
- `[IEO]` The CO's database itself refuses any update that lowers a site's `manifestVersion`,
  whichever code or manual fix issues it.
- `[IEO]` The restore procedure is `co sites raise-versions --by N`, run after the restore. It
  republishes every site's manifest, retired sites included, at its version plus N (N ≥ 1), with
  deployments, digests and bundles unchanged, one site per transaction; rerunning it only raises
  versions further. N MUST exceed the number of manifest versions any site may have published
  since the backup was taken.

## 13. Logging and Observability

### 13.1 Logging Conventions

- Structured (JSON) logs on every tier.
- Every log line about a deployment MUST include `deployment_id`, and `digest` when known.
- LO and EN log lines MUST include `site_id`; EN and host-scoped LO lines MUST include `host_id`.
- Sync attempts MUST log their outcome (§7.4) and `manifest_version`.
- `RejectedRollback` and `AbortedDigestMismatch` MUST be logged at a security/warning level with
  the offending values: the stored and received `manifestVersion`, the expected and computed
  digest, or the bundle entry that does not match the manifest.
- Log sink failures MUST NOT stop orchestration.

### 13.2 Metrics

Each tier exposes Prometheus metrics. REQUIRED names (prefix `ieo_`):

- CO: `ieo_co_manifest_version{site}`, `ieo_co_deployments{site,state}`,
  `ieo_co_api_requests_total{endpoint,code}`, `ieo_co_status_reports_total{site}`
- LO: `ieo_lo_sync_total{outcome}`, `ieo_lo_sync_duration_seconds`,
  `ieo_lo_accepted_manifest_version`, `ieo_lo_reconcile_duration_seconds`,
  `ieo_lo_commands_total{action,result}`, `ieo_lo_outbox_depth`, `ieo_lo_hosts{liveness}`,
  `ieo_lo_last_successful_sync_timestamp_seconds`
- EN: `ieo_en_operations_total{action,result}`, `ieo_en_components{state}`,
  `ieo_en_pull_duration_seconds`

### 13.3 Self-Reporting

When the LO or EN is not deployed as a container, it reports its own resource usage through the
host's collector `[Margo]`.

### 13.4 Health Check `[IEO]`

The CO answers `GET /healthz` on the listener of the Margo API, outside `/api/v1` and without
authentication: `200` while its database answers, `503` when it does not. The response reveals
nothing else.

## 14. Failure Model and Recovery Strategy

### 14.1 Failure Classes

1. `Configuration Failures` — missing REQUIRED key, unreadable certificate, invalid ID characters.
2. `Central Link Failures` — CO unreachable, throttled, retired, or planned downtime.
3. `Integrity Failures` — rollback attempt, digest mismatch, invalid archive.
4. `Site Link Failures` — EN unreachable, command not acknowledged.
5. `Execution Failures` — pull failure, start timeout, container exit.
6. `Restart` — any tier process restarts, with or without its store.

### 14.2 Recovery Behavior

| Situation | Behavior |
| --- | --- |
| Invalid configuration | Exit non-zero at startup with a readable error. |
| CO unreachable | LO backs off (honoring `Retry-After`); keeps the site converged to last accepted state; buffers status in the outbox. On reconnect: one poll brings desired state current; outbox flushes. |
| Planned downtime window | LO does not poll and ignores communication errors during the window. |
| Site retired | LO stops polling; keeps current workloads running until an operator intervenes. |
| `manifestVersion` ≤ stored | Reject; security log; keep previous desired state. |
| Digest mismatch, or bundle not matching the manifest | Abort the whole update; keep previous desired state; retry next poll. |
| Invalid archive | Component `failed` with `IEO-ARCHIVE-INVALID`; LO retries with backoff. |
| EN offline | After missed-heartbeat window: no commands; its deployments `pending`. On return: inventory, then reconcile. |
| Command rejected or unacknowledged | Actual state unchanged; per-deployment retry with backoff. |
| Container crash | Compose restart policy applies; if unrecovered, component `failed`. |
| LO restart | Reload store; re-send capabilities; poll CO; request inventory from all ENs; reconcile. |
| EN restart | Reload store; publish capabilities and inventory; LO reconciles drift. |
| CO restart | Serve from database; no LO action needed. |

### 14.3 Operator Intervention Points

- Create, update or delete deployments through `edgectl`.
- Retire a site.
- Restore the CO database, then raise every site's `manifestVersion` (§12).
- Decommission a host (LO configuration or operator command; implementation-defined).
- Configure polling interval, polling hours and downtime windows at the LO.

## 15. Security and Operational Safety

### 15.1 Trust Boundaries

- CO ↔ LO crosses the internet or a WAN: mutual TLS with SPIFFE X.509 certificates.
- LO ↔ EN stays on the site network: NATS with TLS and per-host credentials.
- Workloads are untrusted with respect to orchestration: they never receive CONTROL credentials.
- Application packages are trusted by the operator who imports them (signature verification is a
  later phase).

### 15.2 Identity and Enrollment

- `edgectl site add` registers the site and issues the LO's certificate from the CA managed with the
  CO deployment `[IEO]`.
- Host enrollment issues per-host NATS credentials; the procedure is implementation-defined and
  MUST produce credentials scoped as in §11.2.
- The CO MUST keep an accepted-client policy and reject unknown or retired clients.

### 15.3 Integrity

- Every deployment YAML, bundle and Compose archive layer is verified against its digest before use.
- The LO rejects any manifest whose version is not strictly greater than the accepted one.

### 15.4 Secret Handling

- Tier credentials (TLS keys, NATS credentials, operator token) MUST NOT appear in logs, status
  messages or workload environments.
- Workload secrets are out of scope for phase 1. Implementations MUST NOT invent a secret channel
  through parameters; parameters are plain configuration.

### 15.5 Hardening Guidance (RECOMMENDED)

- Run EN with the least privilege the container runtime allows (e.g. rootless Podman).
- Restrict the operator API to a management network.
- Rotate the phase 1 operator token manually until OAuth2 is added.

### 15.6 Interim Authentication `[IEO, interim]`

These rules apply from the first slice that adds each interface until Appendix B step 4 (mutual TLS
and scoped NATS credentials) replaces them (ADR 0005):

- CO ↔ LO: each LO MUST present a per-site bearer token issued at site registration
  (`Authorization: Bearer <token>`). The CO MUST map the token to its site, reject unknown tokens,
  and serve only that site's resources.
  - The CO generates the token from 256 random bits, returns it once, and stores only its SHA-256.
    Issuing a new token for a site replaces the old one.
  - Until the operator API registers sites (§11.3), `co site add <site-id>` adds the site and
    prints its token on one line of stdout; it refuses a site that exists.
    `co site rotate-token <site-id>` prints a token that replaces the old one, and refuses a
    retired site. Neither command logs the token.
  - A missing or unknown token gets `401` with problem type `about:blank` and
    `WWW-Authenticate: Bearer`; the LO treats it like `Unreachable` (§7.4).
  - The CO serves the Margo API over plain HTTP. Where the CO ↔ LO path leaves a trusted network,
    a TLS-terminating proxy in front of the CO carries it over HTTPS.
  - The LO sends the token only to an `https://` `lo.co_url`, and verifies the server's
    certificate against `lo.tls.ca_file` when it is set, or the system roots otherwise. It sends
    the token to an `http://` URL only when `lo.co_insecure` is true, and then logs a warning at
    startup that names the URL, never the token. An `http://` URL without `lo.co_insecure` is a
    configuration error: the LO exits at startup (§6.1).
  - The LO follows no redirects on Margo API requests. A redirect (`301`, `302`, `303`, `307`,
    `308`) is `Unreachable` (§7.4); `304` stays `NotModified`.
- LO ↔ EN: ENs MUST authenticate to NATS with per-site username and password. Until scoped
  credentials land, these credentials are shared by the hosts of one site.
- `edgectl` → CO: requests MUST carry the static operator token from configuration.
- Tokens and passwords come from configuration or the environment and follow §15.4; they MUST NOT
  appear in code or test fixtures.

## 16. Reference Algorithms (Language-Agnostic)

### 16.1 CO: Create or Update Deployment

```text
function upsert_deployment(req):
  app = catalog.get(req.app_id, req.version) or reject(404)
  profile = select_profile(app, req.profile_id, type="compose") or reject(400)
  site_id, host_id = split_device_id(req.target)
  checks = preliminary_checks(site_id, host_id, profile.deviceConstraints, req.parameters)
  if checks failed: reject(422, checks.errors)

  tx = db.begin()
  id = req.id or new_uuid()
  doc = build_application_deployment(id, app, profile, req.target, req.parameters)
  bytes = serialize_yaml(doc)
  digest = "sha256:" + hex(sha256(bytes))
  tx.put_blob(digest, bytes)                    # no-op if already stored
  tx.set_deployment(id, site_id, digest, req)
  tx.rebuild_manifest(site_id)                  # increments manifestVersion
  tx.commit()
  return id, digest
```

### 16.2 LO: Service Startup

```text
function start_lo():
  cfg = load_and_validate_config() or exit(1)
  store = open_store(cfg.data_dir)
  state = store.load()          # version, etag, desired, placements, hosts, outbox
  start_site_nats(cfg)
  subscribe("site.<s>.host.*.heartbeat",    on_heartbeat)
  subscribe("site.<s>.host.*.inventory",    on_inventory)
  subscribe("site.<s>.host.*.status",       on_status)
  subscribe("site.<s>.host.*.capabilities", on_capabilities)
  report_capabilities_with_retry()          # gateway first, then hosts
  publish("site.<s>.inventory.request", {})  # ENs answer with full inventory
  schedule(sync_tick, delay=0)
  schedule_every(cfg.reconcile.interval, reconcile_all)
  schedule_every(1s, check_liveness)
  event_loop(state)                          # single authority for state mutations
```

### 16.3 LO: Sync Tick

```text
function sync_tick(state):
  if outside_poll_hours() or in_downtime_window():
    record(SkippedWindow); schedule(sync_tick, cfg.poll.interval); return

  resp = http_get("/api/v1/deployments", if_none_match=state.etag)
  switch resp:
    429 or has Retry-After:  record(Throttled);   schedule(sync_tick, retry_after(resp)); return
    transport_error or 5xx:  record(Unreachable); schedule(sync_tick, backoff()); return   # honors problem backoffStrategy
    4xx with retryable=false (other than 403 retired): record(Unreachable); alert_operator(); stop_polling(); return
    403 retired:             record(Retired);     stop_polling(); return
    304:                     record(NotModified); flush_outbox(); schedule(sync_tick, cfg.poll.interval); return
    200:                     manifest = parse(resp.body)

  if not valid(manifest):                       # schema, unique IDs, bundle present (§8.2 step 3)
    log_error(Unreachable, reason); schedule(sync_tick, backoff()); return

  if state.version != null and manifest.version <= state.version:
    security_log(RejectedRollback, manifest.version, state.version)
    schedule(sync_tick, cfg.poll.interval); return

  missing = [d for d in manifest.deployments if not state.has_blob(d.digest)]
  fetched, err = fetch_bundle_or_individual(manifest, missing)   # raw bytes, not yet unpacked
  if err:                                       # 404 included: never a removal; polling continues
    record(Throttled if err.retry_after else Unreachable)
    schedule(sync_tick, err.retry_after or backoff()); return
  if fetched.is_bundle:
    if sha256(fetched.bytes) != manifest.bundle.digest:          # before unpacking (§15.3)
      security_log(AbortedDigestMismatch, manifest.bundle.digest)
      schedule(sync_tick, cfg.poll.interval); return
    blobs, ok = unpack_bundle(fetched.bytes, manifest)           # ADR 0012; one entry per deployment
    if not ok:
      security_log(AbortedDigestMismatch, offending_entry)
      schedule(sync_tick, cfg.poll.interval); return
  else:
    blobs = fetched.yamls
  for (digest, bytes) in blobs:
    if sha256(bytes) != digest:
      security_log(AbortedDigestMismatch, digest)
      schedule(sync_tick, cfg.poll.interval); return

  new_desired = {}
  for d in manifest.deployments:
    prev = state.desired.get(d.id)
    adopted = prev.adopted if prev and prev.digest == d.digest else manifest.version
    new_desired[d.id] = {digest: d.digest, yaml: blob(d.digest), adopted: adopted}

  store.atomic_put("desired", new_desired); state.desired = new_desired
  reconcile_all(state)
  store.atomic_put("version", manifest.version, "etag", resp.etag)
  record(Accepted)
  flush_outbox()
  schedule(sync_tick, cfg.poll.interval)
```

### 16.4 LO: Reconcile One Host

```text
function reconcile_host(state, host):
  if host.liveness != Online:
    for d in desired_for(host): report_pending(d, host)
    return

  want = {d.id: d.digest for d in resolve_targets(state) if d.host == host.id}
  have = state.actual[host.id].deployments          # id -> {digest, components}

  for id, digest in want:
    h = have.get(id)
    needs_apply = h is null or h.digest != digest or any_failed(h.components)
    if needs_apply and retry_due(host.id, id):
      send_command(host, "apply", id, digest, state.desired[id].yaml)
  for id in keys(have):
    if id not in want and retry_due(host.id, id):
      send_command(host, "remove", id)

function send_command(host, action, id, digest=null, yaml=null):
  ack = nats_request("site.<s>.host.<h>.cmd", Command(...), timeout=cfg.command.ack_timeout)
  if ack timed out or not ack.accepted:
    schedule_retry(host.id, id, error=ack.error or "ack timeout")
  # actual state is NOT changed here; only EN reports change it
```

### 16.5 LO: Resolve Targets and Place

```text
function resolve_targets(state):
  result = []
  for id, d in state.desired:
    site, host = split_device_id(d.target)
    if host != "*":
      if host not in state.hosts or state.hosts[host].liveness == Decommissioned:
        set_status(id, failed, code=101); continue
      if not satisfies(state.hosts[host], d.constraints): set_status(id, failed); continue
      result.append({id, host})
    else:
      if not cfg.placement.enabled: set_status(id, failed, code=103); continue
      p = state.placements.get(id)
      if p and p.host usable and satisfies(state.hosts[p.host], d.constraints):
        result.append({id, p.host}); continue
      candidates = [h for h in online_hosts(state)
                    if satisfies(h, d.constraints.eligibilityRules)
                    and has_unreserved_capacity(h, d.constraints.capacityRequirements)]
      if candidates empty: set_status(id, failed, code="IEO-NO-ELIGIBLE-HOST"); continue
      candidates.sort(by free_memory descending, then host.id ascending)
      h = candidates[0]
      store.put_placement(id, h.id)
      result.append({id, h.id})
  return result
```

### 16.6 EN: Handle Command

```text
function on_command(cmd):
  if cmd.action == "apply":
    if applied.get(cmd.deploymentId).digest == cmd.digest and all_installed(cmd.deploymentId):
      return ack(accepted=true)
    if not valid_deployment(cmd.deployment): return ack(accepted=false, error=...)
    ack(accepted=true)
    run_serialized(cmd.deploymentId, apply_deployment, cmd)
  else:
    ack(accepted=true)
    run_serialized(cmd.deploymentId, remove_deployment, cmd)

function apply_deployment(cmd):
  for c in cmd.deployment.spec.deploymentProfile.components:
    publish_status(cmd, c.name, installing)
    layer = oci_pull(c.repository, c.revision)   # revision is the tag (§4.2)
    if layer failed:                          return fail(cmd, c, "IEO-PULL-FAILED")
    if sha256(layer.bytes) != layer.digest:   return fail(cmd, c, "IEO-DIGEST-MISMATCH")
    dir = safe_extract(layer, component_dir(cmd, c))
    if dir failed:                            return fail(cmd, c, "IEO-ARCHIVE-INVALID")
    write_env(dir, parameters_for(cmd, c) + otel_env())
    r = compose_up(project_name(cmd.deploymentId, c.name), dir, wait=c.wait, timeout=c.timeout)
    if r timed out:                           return fail(cmd, c, "IEO-START-TIMEOUT")
    if r failed:                              return fail(cmd, c, "IEO-COMPOSE-FAILED")
    publish_status(cmd, c.name, installed)
  remove_orphan_projects(cmd)
  store.put_applied(cmd.deploymentId, cmd.digest, project_names)

function fail(cmd, c, code):
  publish_status(cmd, c.name, failed, error={code, source: c.name, message})
  # later components stay pending
  store.put_applied(cmd.deploymentId, cmd.digest, project_names_so_far)
  # inventory now shows this digest with a failed component; the LO retries with backoff
```

### 16.7 EN: Startup

```text
function start_en():
  cfg = load_and_validate_config() or exit(1)
  host_id = load_or_create_host_id(cfg)
  store = open_store(cfg.data_dir)
  connect_nats(cfg, on_reconnect=publish_inventory)
  subscribe("site.<s>.host.<h>.cmd", on_command)
  subscribe("site.<s>.inventory.request", publish_inventory)
  publish_capabilities()
  refresh_component_states_from_runtime()      # containers may have changed while down
  publish_inventory()
  start_container_event_watch()
  schedule_every(cfg.heartbeat.interval, publish_heartbeat)
  schedule_every(cfg.inventory.interval, publish_inventory)
  start_otel_collector()
```

## 17. Test and Validation Matrix

A conforming implementation SHOULD include tests that cover the behaviors in this specification.

Validation profiles:

- `Core Conformance`: deterministic tests REQUIRED for every conforming implementation. They run
  with fakes for the registry, container runtime and clock where needed.
- `Extension Conformance`: REQUIRED only for OPTIONAL features an implementation ships.
- `Site Integration Profile`: tests against real NATS, a real registry and real Docker/Podman,
  RECOMMENDED in CI.
- `Interop Profile`: tests against another Margo implementation, RECOMMENDED before release.

Unless otherwise noted, §17.1–§17.7 are `Core Conformance`.

### 17.1 Contracts and Identifiers

The pinned Margo OpenAPI is the file named in the header, under `api/margo/<commit>/`, copied
unchanged from the upstream commit with a `README.md` giving the repository, full commit SHA and
source path. Contract types are tested against this file, not generated from it. A pin change is a
PR of its own that MUST: name the prior and new commit; summarize the upstream changes to the
management interface; append an endpoint diff table (method, path, `Match | Different | Missing |
Local-Only`, note) to `docs/margo-pins.md`, with a reason for every row that is not `Match`; state
whether the change is compatible, needs an adapter, or is breaking; update the header, the vendored
file and the affected sections; and keep §17.1 green. The Margo API surface has no `Local-Only`
endpoints (§11.3 holds IEO-specific operations).

- Margo API request and response bodies validate against the vendored Margo OpenAPI file.
- Site messages validate against the schemas in §11.2; unknown fields are ignored; invalid messages
  are dropped without state change.
- Site IDs and host IDs with characters outside RFC 3986 unreserved are rejected; `any` is rejected
  as a site ID.
- Device ID parsing splits on the first `/` only.
- Compose project names follow §4.2 for component names with mixed case and symbols.
- Tag-to-SemVer conversion turns `_` into `+`; tag and revision compare as exact strings.

### 17.2 CO: Catalog, Deployments, Manifest

- Importing an invalid Application Description is rejected with the reason (§5.3 cases).
- Re-importing an identical version succeeds without changes.
- Creating a deployment stores YAML under the SHA-256 of its exact bytes.
- Updating a deployment keeps its ID, changes its digest, and increments `manifestVersion` by one.
- Deleting a deployment removes it from the manifest and increments `manifestVersion`.
- Preliminary checks reject: unknown site, retired site, unknown directed host, host failing
  constraints, autonomous target with no eligible host. No deployment is created.
- `manifestVersion` starts at 1 per site and is independent across sites.
- Identical manifest content yields identical bytes and identical ETag (RFC 8785).
- `If-None-Match` with the current ETag returns `304`.
- A site with no deployments gets `bundle: null`.
- Deployment YAML and bundles are served byte-identical to their digests with immutable cache
  headers; the manifest has `Cache-Control: private`.
- A caller sees only its own site's resources; the path cannot be used to read another site.
- A retired site gets `403` with problem type `#not-authorized` on every endpoint.
- A host capability report before the gateway report returns `404 gateway-not-found`.
- Vendor extensions are copied byte-for-byte; `deviceConstraints` are copied unmodified.
- A deleted deployment reported `removed` is marked removed; status history keeps every report.

### 17.3 LO: Sync

- First sync without ETag fetches the bundle and accepts the manifest.
- `304` changes nothing.
- A manifest with `manifestVersion` equal to or lower than stored is rejected, logged as a security
  event, and leaves desired state unchanged.
- A digest mismatch on any artifact, or a bundle with a missing, extra or misnamed entry, aborts the
  whole update and leaves desired state unchanged.
- When the deployments whose digest the LO does not hold are more than half of the manifest's, the
  LO fetches the bundle; otherwise it fetches each of them by its content URL.
- A `404` on a content URL does not remove the deployment, leaves desired state unchanged, and does
  not stop polling.
- A manifest that fails validation (schema, duplicate `deploymentId`, `bundle: null` with
  deployments) is not accepted and leaves desired state unchanged.
- A deployment absent from an accepted manifest is removed from desired state.
- `adoptedManifestVersion` stays at the version where the current digest first appeared, across
  later manifests that do not change that deployment.
- Version and ETag survive LO restart; rollback protection holds after restart.
- `Retry-After` is honored; `retryable: false` stops retrying that request.
- No polling happens outside polling hours or inside a downtime window.
- After a simulated outage spanning several CO-side changes, one successful poll converges desired
  state to the latest manifest.

### 17.4 LO: Reconciliation and Placement

- Desired but absent → Apply sent; present with other digest → Apply sent; present but not desired
  → Remove sent; equal → nothing sent.
- Offline hosts receive no commands and their deployments are reported `pending`.
- A host returning online is reconciled after its inventory arrives.
- Sending a command does not change actual state; only EN reports do.
- Rejected or unacknowledged commands are retried with exponential backoff up to the cap; retries
  stop when the digest is reported `installed` or the deployment is no longer wanted.
- Directed deployment to an unknown host → `failed`, code `101`, source = LO device ID.
- Placement chooses the eligible online host with the most free memory; ties go to the lowest host
  ID; the result is the same across repeated runs.
- Placement is durable across LO restart and is not moved when another host becomes a better fit.
- A decommissioned host's autonomous deployments are re-placed; directed ones report `101`.
- No eligible host → `failed`, `IEO-NO-ELIGIBLE-HOST`, retried on next pass.
- A component reported `failed` at the desired digest triggers a re-Apply after backoff, not
  immediately and not never.
- After LO restart the LO publishes `site.<s>.inventory.request` and every online EN answers.
- Placement disabled → autonomous deployments report `103`.
- No two reconcile passes for the same host run concurrently.

### 17.5 LO: Status and Outbox

- DeploymentStatus has exactly one component entry per component; unreported components are
  `pending`.
- `status.state` follows the precedence in §7.2 for every combination of two component states.
- `deviceId` is the placed host for autonomous deployments.
- Error `source` follows §8.7.
- While the CO is unreachable, the outbox keeps only the latest status per deployment.
- The outbox survives LO restart.
- On reconnect every outbox entry is sent and removed after `2xx`; an entry replaced during sending
  is not removed.

### 17.6 EN: Execution and Safety

- Apply of an already installed digest does nothing and acks `accepted: true`.
- Remove of an unknown deployment acks `accepted: true` and reports `removed`.
- Components are started in listed order; a failure stops later components.
- `wait: true` waits for running containers; exceeding `timeout` fails with `IEO-START-TIMEOUT`.
- Parameter values appear as environment variables only in the listed components.
- Margo OpenTelemetry variables are present in every container.
- Archives are rejected for: two top-level directories; missing `compose.yaml`; `docker-compose.yml`
  instead of `compose.yaml`; absolute path; `..` segment; symlink escaping the directory; hard
  link escaping the directory.
- setuid, setgid and sticky bits are cleared after extraction.
- A layer whose bytes do not match its digest is not extracted.
- Updating to a new digest removes Compose projects of components that no longer exist.
- A container that exits and does not recover moves its component to `failed`; recovery moves it
  back to `installed`.
- After EN restart, inventory reflects the containers actually running.
- Inventory is published on start and on every NATS reconnect.
- Commands for the same deployment are processed one at a time.

### 17.7 Security, CLI, Observability

- The CO rejects a client without a certificate from the configured CA.
- An EN credential cannot publish on another host's subjects or subscribe to another host's `.cmd`.
- No credential, key or token appears in logs or status messages.
- Until mutual TLS, the LO sends its site token only to an `https://` CO URL unless
  `lo.co_insecure` is set, exits at startup on an `http://` URL without it, and follows no
  redirects (§15.6).
- `edgectl` exits non-zero on failure and prints the problem `title` and `detail`.
- `edgectl site add` produces a certificate whose SPIFFE ID matches §4.2.
- Every tier writes each log line as one JSON object.
- Every log line about a deployment carries `deployment_id`, and `digest` once the digest is known.
- Every LO and EN log line carries `site_id`; every EN log line and every host-scoped LO log line
  carries `host_id`.
- Each sync attempt writes one log line with its §7.4 outcome and `manifest_version`, for every
  outcome in §7.4.
- `RejectedRollback` and `AbortedDigestMismatch` are logged at the security/warning level with the
  offending values: the stored and received `manifestVersion`, the expected and computed digest, or
  the bundle entry that does not match the manifest.
- REQUIRED metrics in §13.2 are exposed and change as expected in the scenarios above.
- A log sink failure does not stop orchestration.

### 17.8 Site Integration Profile (RECOMMENDED)

Run with 1 CO, 1 LO and 2 ENs using real NATS, a local OCI registry and Docker or Podman.

- Golden path: import app → deploy directed → `installed` visible via `edgectl` → update version →
  `installed` at new digest → delete → `removed`.
- Autonomous deployment lands on the host with most free memory.
- Site autonomy: block the LO ↔ CO link; verify workloads keep running, a crashed container is
  restored by its restart policy, and a restarted EN is reconciled to the last accepted state. Make
  two changes on the CO; restore the link; verify the site converges to the latest state in one poll.
- Outbox: stop the CO; cause three status changes on one deployment; start the CO; verify one
  status (the latest) arrives.
- Host outage: stop one EN; update its deployment; start the EN; verify convergence.
- LO restart with store intact and with store deleted; verify convergence both times.

### 17.9 Interop Profile (RECOMMENDED)

- The LO syncs against another Margo WFM implementation.
- Another Margo WFM client syncs against the CO.

### 17.10 Goal Coverage

Each goal in §2.1 is traced to the sections that specify it and the §17 bullets that test it. This
section is a traceability table, not a test list: it adds no bullets for `TestSpec_` tests.

Every §2.1 goal MUST have at least one `Core Conformance` bullet; a goal without one is listed under
Gaps. Every §17.1–§17.7 bullet supports at least one row. A change that adds or removes a goal or a
§17 bullet updates this table in the same change.

| Goal (§2.1) | Specified in | Core Conformance | Integration (§17.8–§17.9) |
| --- | --- | --- | --- |
| 1. Margo WM API, central and site tier | §8.1.3, §8.2, §8.3, §11.1 | §17.1 OpenAPI, IDs, device ID parsing; §17.2 ETag, `304`, `bundle: null`, cache headers, caller scoping, retired site, gateway order; §17.3 first sync | §17.9 both bullets |
| 2. Immutable revisions by digest | §8.1, §12 | §17.2 create, update, byte-identical serving | §17.8 golden path |
| 3. Site converged while center unreachable | §8.2, §8.5, §14.2 | §17.3 outage converges in one poll, `404` not removal; §17.4 diff rules, offline/online hosts, re-Apply after `failed`; §17.6 container exit and recovery | §17.8 site autonomy, host outage |
| 4. Reject stale or tampered state | §8.2, §8.9, §15.3 | §17.3 rollback, digest mismatch, rollback after restart; §17.6 layer digest, archive rejection, setuid bits | — |
| 5. Deterministic placement | §8.4, §8.6 | §17.2 no eligible host; §17.4 most free memory, ties, durable, decommission, `IEO-NO-ELIGIBLE-HOST`, `103` | §17.8 autonomous |
| 6. Idempotent Compose Apply/Remove | §5, §8.5, §8.9 | §17.1 Compose project names, tag comparison; §17.4 retries and backoff; §17.6 idempotent Apply and Remove, order, `wait`/`timeout`, parameters, OTel variables, update cleanup, one command at a time | §17.8 golden path |
| 7. Status host → site → center with buffering | §7.2, §8.1.2, §8.7, §8.8, §10 | §17.2 status history, `removed`; §17.5 all bullets | §17.8 outbox |
| 8. Recovery from durable state, no replay | §12, §14 | §17.3 version and ETag survive restart; §17.4 placement survives, inventory request after restart; §17.5 outbox survives; §17.6 inventory after restart and reconnect | §17.8 LO restart, site autonomy |
| 9. Structured logs and metrics | §7.4, §13 | §17.7 JSON log lines, log fields, sync outcome log, security-level log, metrics, log sink failure | — |
| Cross-cutting: security (§15) | §11.2, §15 | §17.1 site messages; §17.7 client certificate, NATS scoping, no secrets in logs, SPIFFE ID | — |
| Cross-cutting: operator interface | §11.3 | §17.7 `edgectl` errors | §17.8 golden path |

Gaps: none.

## 18. Implementation Checklist (Definition of Done)

### 18.1 REQUIRED for Conformance

- Contract types and validation for the Margo API subset in §11.1 and the messages in §11.2
- CO catalog import from an OCI registry with the validation in §5.3
- CO deployment create/update/delete with immutable digest storage and per-site `manifestVersion`
- CO Margo API server with mutual TLS, caller identification from SPIFFE ID, ETag/`304`,
  bundles, cache headers and RFC 9457 errors
- CO operator API and `edgectl` commands in §11.3
- LO sync loop with rollback protection, digest verification, atomic desired-state replacement and
  durable version/ETag
- LO capability reporting in the required order
- LO target resolution, deterministic durable placement, and host reconciliation with per-deployment
  backoff
- LO status mapping and durable outbox
- LO site NATS server with per-host scoped credentials
- EN idempotent Apply/Remove, archive safety, parameter and OTel variable injection, Compose
  execution with `wait`/`timeout`, container monitoring
- EN capabilities, heartbeat, inventory on start/reconnect/interval
- Structured logs and metrics in §13
- Core Conformance tests in §17.1–§17.7

### 18.2 RECOMMENDED Extensions (Not REQUIRED for Conformance)

- Data plane (`docs/proposals/data-plane.md`, ADR 0006)
- Dynamic configuration reload (§6.2)
- `edgectl deployment status --watch` over server-sent events
- TODO: Helm deployment type on k3s
- TODO: automated certificate renewal and revocation once Margo specifies it
- TODO: application package signature verification
- TODO: placement beyond most-free-memory (spreading, GPUs, affinity to services)

### 18.3 Operational Validation Before Production (RECOMMENDED)

- Run the Site Integration Profile (§17.8) on the target OS and container runtime.
- Run the Interop Profile (§17.9) against at least one other Margo implementation.
- Verify CO database restore keeps `manifestVersion` increasing (§12).

## Appendix A. Data Plane Extension (OPTIONAL)

Moved to `docs/proposals/data-plane.md` §2 (ADR 0006).

## Appendix B. Migration Notes for This Repository

The current code predates this specification. It is replaced, not migrated: new packages are built
from this document and the old Git-based code is deleted as each slice replaces it (ADR 0002).

Implementations in this repository SHOULD build in vertical slices, each one running end to end
from `edgectl` through CO, LO and EN, in this order (details in `docs/roadmap.md`):

1. Golden path v0: contract types and test harness (§11, §17.1), then CO deployment and manifest,
   LO sync, LO planning with EN Compose execution, and status to `edgectl`, one slice per change.
2. Golden path complete: update, delete, autonomous placement, host liveness.
3. Site autonomy and recovery: status outbox, retries, restart recovery, observability.
4. Mutual TLS and scoped NATS credentials, replacing the interim token rules (ADR 0005).
5. Data plane: see `docs/proposals/data-plane.md` (ADR 0006).

After each slice, the parts of the §17.8 golden path it covers MUST stay green.
