# System Overview — Intelligent Edge Orchestrator

| | |
| --- | --- |
| Status | Draft v0.3 |
| Last updated | 2026-09-30 |
| Margo baseline | Margo Specification pre-draft, Workload Management API `1.0.0-rc.3` ([margo/specification](https://github.com/margo/specification), commit `f209a7f`, 2026-09-16) |
| Audience | Contributors and coding agents. Read this before any feature spec. |

This document describes **how the system works**: its parts, their responsibilities, the contracts between them, and the end-to-end flows. It is not a requirements list. The implementation contract (normative requirements, reference algorithms and conformance tests) lives in [`SPEC.md`](../SPEC.md). When a feature changes anything described here, the same change updates this document.

Words in **MUST / SHOULD / MAY** follow RFC 2119. Where this document repeats a Margo rule, the Margo specification is authoritative; where it defines something Margo leaves open (marked **[IEO]**), this document is authoritative.

---

## 1. Purpose

Intelligent Edge Orchestrator (IEO) deploys and manages containerized applications across many **sites** (factories, stores, plants), each with a handful of **hosts**, over networks that are slow, metered or intermittently unavailable.

Design goals:

1. **Margo-conformant** at the central-to-site boundary, so the central manager and the site software can interoperate with other Margo implementations.
2. **Site autonomy.** A site keeps its workloads running and converged while disconnected from the central manager, and catches up in one step when reconnected.
3. **State-based, not command-based.** Every tier converges toward a declared desired state; retries and recovery fall out of reconciliation rather than message replay.
4. **Cooperating workloads.** Applications, in particular AI models, can call each other, exchange data and share results within a site and across sites, over a data plane kept separate from orchestration (proposed in `docs/proposals/data-plane.md`).
5. **Simple first.** Compose on OCI container runtimes first; Helm/Kubernetes later.

The system has two planes:

- **Management plane:** decides and reports *what runs where* (CO ↔ LO ↔ EN). Sections 3–10.
- **Data plane:** carries *the applications' own traffic* between workloads (model-to-model calls, data streams, shared artifacts). Section 11. Margo does not cover this plane; it is entirely **[IEO]**.

## 2. Glossary

| Term | Meaning | Margo equivalent | Code name |
| --- | --- | --- | --- |
| **CO** — Central Orchestrator | The central control plane. One per organization. | Workload Fleet Manager (WFM) | `cmd/co` |
| **LO** — Local Orchestrator | The site controller. One per site. Talks to the CO, manages the site's hosts. | WFM Client acting as a **see-thru gateway** | `cmd/lo` |
| **EN** — Edge Node agent | The agent on each host that runs workloads. | Child device behind the gateway (no Margo client of its own) | `cmd/en` |
| **Site** | A physical location served by one LO. | The scope of one WFM Client | — |
| **Host** | A machine at a site that runs workloads. Runs one EN. | Child device | — |
| **`edgectl`** | Operator CLI. | — (vendor-specific) | `cmd/edgectl` |
| **Application Registry** | OCI registry holding application packages. | Application Registry | — |
| **Application Description** | An app's package descriptor (`margo.yaml`): profiles, components, parameters, device constraints. | ApplicationDescription | — |
| **Deployment** | One app instance assigned to one target (a host, or "any host at site X"). Identified by a UUID that never changes; its content is identified by a digest that changes on every edit. | ApplicationDeployment | — |
| **State Manifest** | The list of all deployments assigned to one LO, with a monotonically increasing `manifestVersion`. | State Manifest | — |
| **Component** | One deployable unit inside a deployment (for Compose: one Compose package). | Component | — |
| **Directed deployment** | Target names a specific host: `deviceId: <lo-id>/<host-id>`. | Gateway-directed | — |
| **Autonomous deployment** | Target lets the LO choose the host: `deviceId: <lo-id>/*`. | Gateway-autonomous | — |
| **Data plane** | Messaging between workloads (not orchestration). | — | — |
| **Hub** | Central NATS cluster that links every site's data plane. | — | — |
| **Service** | A workload endpoint other workloads call by name with request/reply, e.g. a model offering `classify`. | — | — |
| **Topic** | A named stream of data a workload publishes, e.g. `production.counts`. | — | — |

## 3. Architecture

```
  Operator                 App Developer
     │                          │ push package (OCI)
     ▼                          ▼
 ┌─────────┐   REST    ┌──────────────────┐   OCI Distribution   ┌──────────────────────┐
 │ edgectl │ ────────▶ │        CO        │ ───────────────────▶ │ Application Registry │
 └─────────┘  [IEO]    │  (Margo WFM)     │    pull packages     └──────────────────────┘
                       │  Postgres        │
                       └────────▲─────────┘
                                │ Margo Workload Management API
                                │ HTTPS/1.1 :443, mutual TLS, LO polls
          ┌─────────────────────┼─────────────────────┐
          │                     │                     │
   ┌──────┴──────┐       ┌──────┴──────┐       ┌──────┴──────┐
   │ LO  site A  │       │ LO  site B  │  ...  │ LO  site N  │   Margo see-thru gateway
   │ BoltDB      │       │ BoltDB      │       │ BoltDB      │
   │ site NATS ──┼───┐   │ site NATS ──┼───┐   │ site NATS ──┼───┐   data plane: outbound
   └──────┬──────┘   │   └─────────────┘   │   └─────────────┘   │   leaf-node links
          │ NATS     │                     ▼                     │
   ┌──────┼───────┐  └──────────▶ ┌────────────────┐ ◀──────────┘
   ▼      ▼       ▼               │ Hub (central   │  aggregated streams,
 ┌─────┐ ┌─────┐ ┌─────┐          │ NATS cluster)  │  cross-site services
 │ EN  │ │ EN  │ │ EN  │          └────────────────┘
 └─────┘ └─────┘ └─────┘
   one per host: Docker/Podman Compose + OTel collector; workloads use the site NATS data plane
```

The site NATS server carries two isolated accounts: **CONTROL** (LO ↔ EN orchestration messages, §6.3) and **DATA** (workload traffic, `docs/proposals/data-plane.md`). Only the DATA account is linked to the hub.

### 3.1 Responsibilities

| Tier | Owns | Does | Does not |
| --- | --- | --- | --- |
| **CO** | App catalog, site (client) registry, device inventory, deployments, per-site State Manifests, deployment status history | Imports apps from the registry; validates and creates deployments; publishes manifests; serves the Margo API; records capabilities and status; serves `edgectl` | Talk to hosts; push to sites; choose hosts for autonomous deployments |
| **LO** | The site's copy of desired state, actual state per host, host registry, status outbox; the site NATS server | Polls the CO; verifies and stores manifests; reports capabilities for itself and its hosts; places autonomous deployments; reconciles hosts; aggregates and reports status; keeps the site converged while offline; issues each workload's scoped data-plane credentials | Run workloads itself (the LO is a non-hosting gateway); invent desired state; read or alter workload data |
| **EN** | The host's running workloads and their local state | Reports capabilities, heartbeat and inventory; applies and removes deployments with Compose; injects data-plane connection settings; monitors containers; reports component status; runs the OpenTelemetry collector | Talk to the CO; decide what should run |
| **Hub** | Aggregated data streams; cross-site routing | Accepts outbound leaf-node links from sites; merges site streams; routes cross-site service calls allowed by policy | Carry orchestration traffic |
| **edgectl** | Nothing persistent | Operator commands against the CO API | Talk to LOs or ENs |

### 3.2 Technology

| Concern | Choice |
| --- | --- |
| Language | Go |
| CO API server | Gin; Postgres via ent, Atlas migrations |
| LO / EN local store | BoltDB (embedded) |
| CO ↔ LO | Margo Workload Management API over HTTPS with mutual TLS |
| LO ↔ EN | NATS (core NATS; see §6.3) |
| Data plane | NATS: site server per LO, JetStream for buffered streams and object store, leaf-node links to a central hub cluster (`docs/proposals/data-plane.md`) |
| Registry access | OCI Distribution API (e.g. `oras-go`) |
| Workload runtime (phase 1) | Docker or Podman with Compose |
| Observability | OpenTelemetry collector per host; Prometheus metrics and structured logs (zap) for IEO's own services |
| CLI | Cobra |

## 4. Identity and naming

### 4.1 Device IDs

Margo device IDs are hierarchical and scoped to the reporting client.

- The LO's own device ID is its **site ID**, e.g. `lo-chennai`.
- A host's device ID is `<site-id>/<host-id>`, e.g. `lo-chennai/host-03`.
- IDs use only RFC 3986 unreserved characters (`A–Z a–z 0–9 . _ ~ -`) per segment.
- **[IEO]** `host-id` is stable for the life of the host and is persisted by the EN on first start (a generated ID unless one is configured).

### 4.2 Workload identity (CO ↔ LO)

Per the Margo Identity and Authorization Framework:

- The CO holds an X.509-SVID with SPIFFE ID `spiffe://<trust-domain>/margo/wfm/<wfm-id>`.
- Each LO holds an X.509-SVID with SPIFFE ID `spiffe://<trust-domain>/margo/wfm/<wfm-id>/client/<site-id>`.
- Every Margo API call uses mutual TLS (TLS 1.3 by default). The CO identifies the caller **only** from the SVID, never from the path or body, and only exposes that caller's own resources.
- The CO keeps an accepted-client policy; a site that is retired gets `403` with the Margo `#not-authorized` problem type, titled "Client Relationship Retired". Until mTLS (Appendix B step 4) the caller is identified by a per-site bearer token whose SHA-256 the CO keeps (§15.6).
- **[IEO]** Enrollment is operator-driven: `edgectl site add` registers the site and issues the LO's SVID from a CA managed by the CO deployment. Automated renewal is future work (Margo has not specified it yet).

### 4.3 Site-internal identity (LO ↔ EN) [IEO]

- NATS runs with TLS and per-host credentials (NKEY or user JWT) in the **CONTROL** account, issued when a host joins the site.
- Each EN may publish only on its own subjects (`site.<site-id>.host.<host-id>.>`) and subscribe only to its own command subject and to the site-wide inventory request (`site.<site-id>.inventory.request`). The LO has access to all host subjects of its site.
- Workloads never receive CONTROL credentials. Their data-plane identity is described in `docs/proposals/data-plane.md` §1.6.

## 5. End-to-end flows

### 5.1 Publish and import an application

1. The App Developer pushes an application package (Application Description plus resources) to the Application Registry as an OCI artifact, one tag per `metadata.version`.
2. The operator runs `edgectl app import --repo <registry>/<namespace>/<app>`.
3. The CO lists tags, pulls the manifest and blobs for each selected version, validates the Application Description and stores it in the catalog.

Each Compose component in an Application Description points to an OCI artifact (`repository: oci://…`, `revision: <semver>`) holding the Compose package.

### 5.2 Onboard a site and its hosts

1. The operator registers the site: `edgectl site add <site-id>`. The CO creates the client record and issues the LO's credentials (§4.2).
2. The LO starts and reports **its own** capabilities: `PUT /api/v1/capabilities/<site-id>`. The LO is non-hosting, so it sends identity fields only (`id`, `vendor`, `modelNumber`, `serialNumber`) and omits the hosting fields.
3. Each EN joins the site's NATS and publishes its capabilities (§6.3).
4. For each host, the LO calls `PUT /api/v1/capabilities/<site-id>/<host-id>` with that host's full capabilities: `cpus`, `memory`, `storage`, `peripherals`, `interfaces`, `otelCollector: true`, `supportedRuntimes: [oci]`, `supportedDeploymentTypes: [compose]`, plus `labels`.
5. The gateway's own report MUST reach the CO before any host report; the CO rejects a host report for an unknown gateway with `404 gateway-not-found`.
6. When a host changes (hardware, labels), the LO re-sends its capabilities. When a host is decommissioned, the LO calls `DELETE /api/v1/capabilities/<site-id>/<host-id>`.

### 5.3 Create a deployment

1. The operator runs, for example:
   - Directed: `edgectl deploy --app <app-id> --version 1.2.1 --target lo-chennai/host-03 --param siteId=SID-1`
   - Autonomous: `edgectl deploy --app <app-id> --version 1.2.1 --target lo-chennai/* --param siteId=SID-1`
2. The CO builds an ApplicationDeployment:
   - `id`: new UUID (kept for the life of the deployment; edits keep the ID and change the digest)
   - `metadata.name`, `metadata.namespace`, `metadata.deviceId` (the target)
   - `spec.applicationId`, `spec.deploymentProfile` (type `compose`, components copied from the Application Description, `deviceConstraints` copied **unmodified**)
   - `spec.parameters` (user values keyed by the parameter names in the Application Description, with their targets)
   - vendor `x-…-extensions` copied from the Application Description exactly as they appear
3. The CO performs preliminary checks: the target site exists; for a directed deployment the host exists and its reported capabilities satisfy `capacityRequirements` and `eligibilityRules`; for an autonomous deployment at least one host at the site satisfies them. Failing checks reject the request; they never produce a deployment.
4. The CO serializes the YAML once, stores it immutably under its SHA-256 digest, adds it to the site's State Manifest, and increments that site's `manifestVersion`.

Updating a deployment (new app version, new parameter values) produces new YAML bytes, a new digest and a new `manifestVersion`; the deployment ID stays the same. Deleting a deployment removes it from the manifest and increments `manifestVersion`.

### 5.4 Site synchronization (LO ↔ CO)

The LO runs this loop at the configured polling rate (§9):

1. `GET /api/v1/deployments` with `If-None-Match: <last ETag>` (omitted on first sync).
2. `304 Not Modified` → nothing to do.
3. `200 OK` → validate:
   - `manifestVersion` MUST be strictly greater than the stored version. Otherwise reject the manifest and log a security event (rollback protection).
4. Fetch content for every deployment whose digest the LO does not already hold:
   - many changes or first sync → `GET` the bundle (`/api/v1/bundles/{digest}`);
   - few changes → `GET /api/v1/deployments/{id}/{digest}` for each.
5. Verify the SHA-256 digest of every fetched artifact. **Any mismatch aborts the whole update; the LO keeps its previous desired state.**
6. Store the new desired state atomically, then reconcile the site (§5.5).
7. Once reconciliation has been started for every deployment, durably persist the new `manifestVersion` and ETag.

A deployment that is **absent** from the manifest is to be removed. A `404` on a content URL only means that digest is unavailable; it is never a removal signal.

**Catching up after an outage** needs nothing extra: the first successful poll returns the complete current manifest, and the LO converges to it. Intermediate manifests the LO never saw do not matter.

### 5.5 Site reconciliation (LO → EN)

The LO holds, per host, the **desired** set of deployments (from the manifest plus placement decisions) and the **actual** set (from EN inventory reports). A reconcile pass runs when the desired state changes, when an EN reports a change, when a host comes back online, and periodically as a safety net.

1. **Resolve targets.**
   - Directed (`<site-id>/<host-id>`): the host is the target. An unknown host produces status `failed` with error code `101` (unknown child device ID).
   - Autonomous (`<site-id>/*`): the LO chooses a host (§5.6) and records the choice durably. A placement is kept for the life of the deployment unless its host is decommissioned or stops satisfying the constraints.
2. **Evaluate constraints.** Before applying, the LO MUST evaluate `eligibilityRules` against the host's capabilities and SHOULD check `capacityRequirements`. A directed deployment whose host fails them gets status `failed`.
3. **Diff per host.** For each host that is alive:
   - deployment desired but not present, or present with a different digest → send **Apply**;
   - deployment present but not desired → send **Remove**;
   - otherwise nothing.
4. **Skip offline hosts.** Hosts without a recent heartbeat receive nothing; their deployments are reported `pending` and the host is reconciled when it returns.
5. **Actual state changes only on EN reports.** A failed or unacknowledged operation leaves desired ≠ actual, so the next pass retries it. Apply and Remove MUST be idempotent on the EN.

Operations are **whole-deployment** (Apply the deployment at digest X / Remove deployment Y). Converging the individual components inside a deployment is the EN's job (Compose already does this), which keeps the LO simple and the protocol small.

### 5.6 Autonomous placement [IEO]

For `deviceId: <site-id>/*` the LO chooses among hosts that are alive, satisfy the deployment's `eligibilityRules`, and have enough unreserved capacity for `capacityRequirements`. Among those it picks the host with the most free memory, breaking ties by host ID so the choice is deterministic. If no host qualifies, the deployment is reported `failed` with an IEO error code (`IEO-NO-ELIGIBLE-HOST`) and retried on the next reconcile pass.

A gateway that cannot place autonomously MUST report error `103` (autonomous placement not supported). IEO supports autonomous placement, so it reports `103` only if placement is disabled by configuration.

### 5.7 Execution on the host (EN)

On **Apply** for a Compose deployment, the EN:

1. Pulls each component's **Margo Compose Archive** from its `oci://` repository at tag `revision` (the revision is the tag; build metadata is written after `_`, SPEC §4.2). The OCI manifest has `artifactType: application/vnd.org.margo.component.compose+json` and a single layer of type `application/vnd.org.margo.component.compose.tar+gzip`. The EN MUST verify the layer's OCI digest before extracting.
2. Validates the archive before extracting: exactly one top-level directory containing `compose.yaml` (other file names are invalid); no absolute paths, no `../`, no links pointing outside the top-level directory; setuid, setgid and sticky bits are stripped. A violation fails the component.
3. Applies the deployment's parameters: for Compose, each target `pointer` is the **name of an environment variable** set for the listed components. **[IEO]** The EN writes them into the Compose project's environment file. Secrets are never taken from the archive; provisioning them is out of scope for phase 1.
4. Injects the OpenTelemetry collector connection variables required by Margo into every container, and, for deployments that declare data-plane use, the data-plane settings and credentials file from the Apply command (`docs/proposals/data-plane.md` §1.6).
5. Runs the components in the order listed, as one Compose project per component named `<deployment-id>-<component-name>`. When `wait` is true (the default, which Margo requires clients to support), it waits until all containers are running before starting the next component, failing the component if `timeout` elapses.
6. Reports component states as they change: `installing` → `installed` or `failed` (with error code, source = component name, message).

On **Remove**, the EN brings down the deployment's Compose projects, reporting `removing` → `removed`.

**Monitoring.** The EN watches container events for every deployment it runs. A container that exits unexpectedly and does not recover under its Compose restart policy moves its component to `failed`, reported with the container's exit information. The EN also runs the device's OpenTelemetry collector (host metrics and container metrics receivers, OTLP receiver) with **no exporters pre-configured**; the end user decides where observability data is exported.

### 5.8 Status reporting (EN → LO → CO)

1. The EN publishes component status events and a periodic full **inventory** (every deployment it runs, with digest and component states).
2. The LO maps these onto Margo deployment status and calls `POST /api/v1/deployments/{deploymentId}/status` with:
   - `deploymentId`
   - `deviceId`: the host's full ID (`<site-id>/<host-id>`); for an autonomous deployment, the host the LO chose
   - `adoptedManifestVersion`: the `manifestVersion` of the manifest the LO took this deployment's current revision from (not simply the latest manifest fetched)
   - `components`: exactly one entry per component in the deployment, each with `state` and optional `error`
   - `status.state`: the most severe component state, precedence `failed > removing > installing > pending > installed` (then `removed`)
3. States are `pending`, `installing`, `installed`, `removing`, `removed`, `failed`. Error `source` is the component name for component errors, `metadata.name` for deployment-level errors, and the gateway's device ID for gateway-generated errors (`101`, `102`, `103`).
4. **[IEO] Offline buffering.** Margo does not yet define status reporting across long disconnections. While the CO is unreachable, the LO keeps a durable **outbox with the latest status per deployment** (newer reports replace older ones). On reconnect it sends each entry, then clears it once the CO acknowledges.

### 5.9 Removal and decommissioning

- **Deployment removed** by the operator → absent from the next manifest → LO sends Remove to the host → `removing` → `removed` reported → the CO marks the deployment removed.
- **Host decommissioned** → the LO sends `DELETE` capabilities for it. Autonomous deployments placed there are re-placed; directed deployments to it report `failed` with error `101`.
- **Site retired** → the CO returns `403 #not-authorized` ("Client Relationship Retired"); the LO stops polling and keeps running its current workloads until an operator intervenes.

## 6. Interfaces

### 6.1 edgectl ↔ CO (REST, [IEO])

Not covered by Margo. JSON over HTTPS, authenticated operator sessions (OAuth2 via Keycloak is planned; phase 1 uses a static API token). Resources:

| Resource | Operations |
| --- | --- |
| `/apps` | import from registry, list, get, delete |
| `/sites` | add (issues LO credentials), list, get, retire |
| `/devices` | list, get (read-only; populated from capability reports) |
| `/deployments` | create (app, version, profile, target, parameters), update, delete, list, get |
| `/deployments/{id}/status` | get current status and history |
| `/deployments/{id}/stream` | server-sent events of status changes |

### 6.2 CO ↔ LO (Margo Workload Management API 1.0.0-rc.3)

Server: CO. Client: LO. HTTPS over HTTP/1.1, port 443, mutual TLS. Errors are RFC 9457 `application/problem+json` with Margo problem types; the client MUST honor `Retry-After` and the `retryable` / `backoffStrategy` fields.

| Endpoint | Direction | Purpose |
| --- | --- | --- |
| `PUT /api/v1/capabilities/{deviceId}` | LO → CO | Report capabilities of the LO (`<site-id>`) or a host (`<site-id>/<host-id>`) |
| `DELETE /api/v1/capabilities/{deviceId}` | LO → CO | Unregister a host |
| `GET /api/v1/deployments` | LO ← CO | State Manifest (`application/vnd.margo.manifest.v1+json`), with ETag / `304` |
| `GET /api/v1/deployments/{deploymentId}/{digest}` | LO ← CO | One ApplicationDeployment YAML (immutable, content-addressed) |
| `GET /api/v1/bundles/{digest}` | LO ← CO | All deployment YAMLs as a gzip tar (immutable, content-addressed) |
| `POST /api/v1/deployments/{deploymentId}/status` | LO → CO | Deployment status |

CO obligations beyond the endpoint list:

- `manifestVersion` starts at 1 per site and strictly increases on every change to that site's deployment set.
- The manifest is serialized deterministically (RFC 8785) so identical content yields an identical ETag; the ETag is the quoted `sha256:<hex>` of the response bytes.
- Deployment YAML and bundles are served byte-for-byte identical to their digests, with `Cache-Control: private, max-age=31536000, immutable`; the manifest with `Cache-Control: private` only.
- If there are no deployments, `bundle` is present and `null`.
- A site sees only its own content: another site's deployment YAML or bundle gets the same `404` as one that doesn't exist.

### 6.3 LO ↔ EN (NATS, [IEO])

Subjects (all JSON payloads; `<s>` = site ID, `<h>` = host ID):

| Subject | Direction | Pattern | Payload |
| --- | --- | --- | --- |
| `site.<s>.host.<h>.cmd` | LO → EN | request / reply | `Command` → `CommandAck` |
| `site.<s>.host.<h>.status` | EN → LO | publish | `ComponentStatusEvent` |
| `site.<s>.host.<h>.inventory` | EN → LO | publish (on start, on reconnect, every 60 s) | `Inventory` |
| `site.<s>.host.<h>.heartbeat` | EN → LO | publish (every 10 s) | `Heartbeat` |
| `site.<s>.host.<h>.capabilities` | EN → LO | publish (on start and on change) | `Capabilities` |
| `site.<s>.inventory.request` | LO → all ENs | publish | `{}`; every EN answers by publishing its `Inventory` |

Messages:

```jsonc
// Command (LO → EN)
{ "commandId": "uuid", "action": "apply" | "remove",
  "deploymentId": "uuid", "digest": "sha256:…",
  "deployment": { /* ApplicationDeployment, present for "apply" */ },
  "dataPlane": { "url": "tls://…", "creds": "…" } }   // present only if the deployment declares data-plane use (data-plane proposal §1.6)

// CommandAck (EN → LO): acceptance only; outcomes arrive as status events
{ "commandId": "uuid", "accepted": true, "error": { "code": "…", "message": "…" } }

// ComponentStatusEvent (EN → LO)
{ "deploymentId": "uuid", "digest": "sha256:…", "component": "name",
  "state": "pending|installing|installed|removing|removed|failed",
  "error": { "code": "…", "source": "…", "message": "…" }, "at": "RFC 3339" }

// Inventory (EN → LO): the EN's full actual state
{ "hostId": "host-03", "at": "RFC 3339",
  "deployments": [ { "deploymentId": "uuid", "digest": "sha256:…",
                     "components": [ { "name": "…", "state": "…" } ] } ] }

// Heartbeat (EN → LO)
{ "hostId": "host-03", "at": "RFC 3339", "uptimeSeconds": 12345 }

// Capabilities (EN → LO): Margo DeviceCapabilitiesManifest properties plus labels
{ "hostId": "host-03", "capabilities": { /* cpus, memory, storage, … (§5.2) */ }, "labels": { "line": "2" } }
```

Design decisions:

- **Core NATS, not a durable queue.** Missed messages are recovered by state, not replay: an EN publishes its full inventory whenever it (re)connects, and the LO reconciles against it. This keeps a reconnecting host correct even if every message during the outage was lost.
- **Commands are idempotent.** Applying a digest that is already running, or removing something already gone, succeeds without side effects.
- The **LO is the only writer of desired state**; the EN never changes what should run.

## 7. State and persistence

| Where | Store | Contents |
| --- | --- | --- |
| CO | Postgres | Apps and versions; sites and accepted-client policy; devices and latest capabilities; deployments (ID, current digest, target, parameters); immutable deployment YAML by digest; per-site manifest and `manifestVersion`; status history |
| LO | BoltDB | Last accepted `manifestVersion` and ETag; desired deployments (YAML by digest); autonomous placement decisions; hosts (capabilities, last heartbeat); actual state per host (from inventory); status outbox |
| Site NATS (run by LO) | JetStream | DATA account: site topic stream `DATA_<site>`, object store `obj-<site>` (`docs/proposals/data-plane.md`) |
| Hub | JetStream | Aggregated stream `DATA_ALL`; account configuration managed by the CO |
| EN | BoltDB + container runtime | Host ID; deployments applied (ID, digest, Compose project names); last reported component states |

The LO MUST persist `manifestVersion` and ETag durably so that rollback protection survives restarts. If the LO loses its local state, it resynchronizes from the CO (full manifest) and from ENs (inventories); the version it reports is then the one it resynchronized against.

## 8. Failure and offline behavior

| Situation | Behavior |
| --- | --- |
| CO unreachable from LO | LO keeps polling with backoff (respecting `Retry-After`), keeps the site converged to the last accepted desired state, buffers status in the outbox. On reconnect: one poll brings desired state current; outbox flushes. |
| Planned downtime window | LO does not poll and ignores communication errors during the configured window. |
| Manifest with `manifestVersion` ≤ stored | Rejected; security event logged; previous desired state kept. |
| Digest mismatch on any artifact | Whole update aborted; previous desired state kept; retried on next poll. |
| EN unreachable from LO | Host marked offline after 3 missed heartbeats; no commands sent; its deployments reported `pending`. On return: EN publishes inventory, LO reconciles. |
| Command fails on EN | Component reported `failed` with error; actual state unchanged; LO retries on subsequent reconcile passes with exponential backoff per deployment. |
| Container crashes | Compose restart policy applies; if it does not recover, component reported `failed`. |
| LO restarts | Reloads BoltDB, re-polls CO, requests inventory from all ENs (`site.<s>.inventory.request`), reconciles. |
| EN restarts | Reloads its record of applied deployments, publishes inventory; LO reconciles any drift. |
| Site loses its hub link | Site-local services, topics and object store keep working. Outgoing topic data is buffered in the site stream (24 h default) and forwarded on reconnect. Cross-site service calls fail fast with "no responders"; callers must handle it (`docs/proposals/data-plane.md` §1.7). |
| Hub unavailable | Same as above for every site; aggregated streams catch up from the site buffers when the hub returns. |

## 9. Configuration defaults [IEO]

| Setting | Default | Notes |
| --- | --- | --- |
| Manifest polling rate | 60 s | Margo requires this to be user-configurable |
| Polling interval period | always | Margo: hours of the day in which polling happens |
| Planned downtime windows | none | Margo: no retries and errors ignored during the window |
| EN heartbeat | 10 s | Offline after 3 missed |
| EN inventory | 60 s and on (re)connect | |
| Reconcile safety-net interval | 5 min | |
| Site topic buffer (data plane) | 24 h or 10 GiB, whichever first | Oldest data dropped first when the limit is hit |
| Maximum message size (data plane) | 1 MiB | Larger payloads go through the object store (`docs/proposals/data-plane.md` §1.2) |
| Default service request timeout | 5 s | Callers may set their own |
| Object store retention | 24 h | Per object, unless the writer sets a TTL |

## 10. Observability

- **Workloads:** each host's OpenTelemetry collector receives OTLP from workloads and collects host and container metrics. No exporters are pre-configured; operators add exporters to ship data off the device.
- **IEO services:** CO, LO and EN expose Prometheus metrics (sync latency, manifest version, reconcile duration, operations by result, outbox depth, hosts online) and emit structured JSON logs.
- The LO and EN, when not deployed as containers, report their own resource usage through the host's collector, as Margo requires of WFM clients.

## 11. Data plane [IEO]

Moved to `docs/proposals/data-plane.md` §1 (ADR 0006).

## 12. Scope

**Phase 1 (this overview):** Compose deployment type on OCI container runtimes (Docker, Podman); see-thru gateway with directed and autonomous placement; operator-provisioned identities.

**Phase 2 (designed here):** the data plane (`docs/proposals/data-plane.md`): site NATS DATA account, hub with leaf-node links, services, topics, object store, declared and approved permissions.

**Later:** Helm deployment type on k3s; custom runtimes via Margo extensions; automated SVID renewal and revocation; direct site-to-site data links; web portal; application package signature verification.

**Not planned:** opaque-gateway mode; hosts talking to the CO directly.

## 13. Differences from the current code

The current code was built around Git-based delivery. Moving to this design changes:

| Area | Current | Target |
| --- | --- | --- |
| CO → LO delivery | CO commits `desiredstate.yaml` to a Git repo; LO polls Git | Margo API: LO polls `GET /api/v1/deployments` |
| App source | Git app registry (`margo.yaml` in a repo) | OCI Application Registry |
| Deployment document | `apiVersion: margo.edge/v1`, `kind: Deployment`, parameters as a list | Margo ApplicationDeployment (`id`, `metadata.deviceId`, parameters as a map) |
| Device registration | IEO-specific `/register` endpoints | Margo `PUT /api/v1/capabilities/{deviceId}` |
| LO → EN operations | Fine-grained add/update/remove app and component, subject `site.<s>.deploy.<h>` | Whole-deployment apply/remove, subjects in §6.3 |
| EN runtime | containerd plugin running single images | Compose packages via Docker/Podman |
| Security | None between tiers | Mutual TLS with SPIFFE SVIDs (CO ↔ LO); NATS TLS and credentials (LO ↔ EN) |
| Workload-to-workload communication | None | Data plane (`docs/proposals/data-plane.md`): site NATS DATA account, hub, declared permissions |

## 14. Open questions

1. **Placement policy.** "Most free memory" (§5.6) is a starting point; smarter placement (labels, spreading, GPUs) is a future feature spec.
2. **Operator authentication** for `edgectl` in phase 1 (static token vs. Keycloak from the start).
3. **Secrets for Compose workloads.** Margo keeps secrets out of archives and leaves provisioning to the device or WFM; IEO needs a design before workloads that require secrets are supported.
4. **Data volumes.** The 24 h / 10 GiB site buffer and 1 MiB message limit are placeholders; size them against real per-site message rates and payload sizes.
5. **Model-aware placement.** Co-locating models that call each other (`docs/proposals/data-plane.md` §1.7) is manual today (directed deployments, labels). Automatic affinity ("place near service X") would extend §5.6.
6. **Direct site-to-site links** for sites on one campus without internet: when needed, and how they coexist with the hub.
7. **Streaming inference.** Service calls are request/reply. Models exchanging continuous streams (e.g. video frames) with tight latency may need a streaming convention on top of topics; decide when the first such application appears.
8. **Nearest-copy routing.** `docs/proposals/data-plane.md` §1.4 relies on NATS delivering a request to queue-group members on the caller's own server before members reached over the leaf-node link. Verify this with the NATS version in use; if it does not hold, the fallback is a client helper that calls `svc.<own-site>.…` first and `svc.any.…` only on "no responders".
