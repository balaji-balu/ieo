# Proposal: Data plane

Status: Proposal (Phase 2) · Moved here from SPEC Appendix A and system-overview §11 by ADR 0006

This proposal is not part of the Phase 1 contract. It comes back as `[IEO]` sections of `SPEC.md`
and `docs/system-overview.md` when Phase 1 passes SPEC §17.8 (ADR 0006). Until then, references to
"overview §N" mean `docs/system-overview.md` and "SPEC §N" mean `SPEC.md`.

- §1 is the design: how the data plane works and why (was overview §11).
- §2 is the draft contract: the rules an implementation must follow (was SPEC Appendix A).

## 1. Design

The data plane lets workloads, in particular AI models running on ENs, **work together**: one model calls another, models publish results that others consume, and data from many sites is combined for analytics. It is separate from orchestration: workloads cannot see or send orchestration messages, and data traffic cannot delay deployments.

### 1.1 Topology

- **Site server.** Each site runs one NATS server, operated by the LO. Workloads on every host of the site connect to it over the site network. Traffic between workloads at the same site never leaves the site.
- **Hub.** A central NATS cluster, deployed alongside the CO but separate from the CO API. Each site server connects to it as a **leaf node**: the connection is outbound from the site, so plants need no inbound firewall rules.
- **Accounts.** The site server keeps orchestration (CONTROL account) and workload traffic (DATA account) isolated. Only DATA is linked to the hub.
- **No direct site-to-site links in phase 1.** Cross-site traffic goes through the hub. Direct links between nearby sites (e.g. one campus without internet) are a later option; the subject design below does not change for them.

### 1.2 Cooperation patterns

| Pattern | Use it for | Mechanism | Offline behavior |
| --- | --- | --- | --- |
| **Service call** (request/reply) | A model asks another model for an answer: detector → classifier, LLM → retrieval, model ensembles | NATS request/reply to a named service; replicas of a service share load as a queue group | Works within the site while the hub is down; cross-site calls fail fast |
| **Topic stream** (publish/subscribe, buffered) | Continuous results, events and measurements: detections, production counts, model outputs for cross-site analytics | JetStream stream per site, aggregated at the hub | Buffered at the site and forwarded on reconnect |
| **Shared object** | Payloads larger than a message: images, video clips, tensors, model weights, federated-learning updates | JetStream object store per site; messages carry a reference instead of the bytes | Site-local objects always available; cross-site retrieval when connected |

Higher-level cooperation, such as a pipeline of models, an ensemble that votes, or federated learning that trains across sites without moving raw data, is built by applications from these three primitives. The platform does not prescribe the algorithm.

### 1.3 Naming

Data-plane subjects include the site, so every name is unambiguous across the fleet. The one exception is nearest-copy service calls (§1.4), which deliberately leave the site out. `<site>` is the site ID and `<app>` the application ID. The site ID `any` is reserved and cannot be assigned to a site.

| What | Subject / name | Example |
| --- | --- | --- |
| Service endpoint, at a named site | `svc.<site>.<service>.<endpoint>` | `svc.lo-chennai.defect-classifier.classify` |
| Service endpoint, nearest copy | `svc.any.<service>.<endpoint>` | `svc.any.defect-classifier.classify` |
| Topic | `data.<site>.<app>.<topic>` | `data.lo-chennai.line-monitor.detections` |
| Site stream (JetStream) | `DATA_<site>` capturing `data.<site>.>` | `DATA_lo-chennai` |
| Aggregated stream at the hub | `DATA_ALL` sourcing every `DATA_<site>` | subscribe `data.*.line-monitor.detections` for all sites |
| Object store bucket | `obj-<site>` | object name `<app>/<key>` |

Service conventions:

- Requests and replies are JSON unless the service declares another content type in a `Content-Type` header.
- Errors are returned in a reply header `IEO-Error` (code and message), not as a missing reply.
- Every service answers `svc.<site>.<service>.$health` so callers can check it is alive.
- Every reply carries a header `IEO-Served-By: <site>/<host>` so the caller knows which copy answered.
- A service name identifies one service across the whole fleet: the CO rejects an application that declares a service name already provided by a different application ID.

### 1.4 Calling a model at the same site or another site

A model can call another model at its own site or at any other site. There are two ways to address the call.

**Explicit site (default).** The caller names the site in the subject:

| Callee is at… | Caller uses | Route |
| --- | --- | --- |
| The caller's own site | `svc.<own-site>.<service>.<endpoint>` | Stays inside the site's NATS server |
| Another site | `svc.<other-site>.<service>.<endpoint>` | Site NATS → hub → other site's NATS |

This is predictable: the caller always knows whether a call is local (fast, works offline) or remote (slower, needs the hub). There is no implicit fallback from one site to another.

**Nearest copy (opt-in per service).** A service can additionally be offered with `discovery: nearest`. Callers then use `svc.any.<service>.<endpoint>` and do not name a site. The call is answered by:

1. a copy at the caller's own site, if at least one is running; otherwise
2. a copy at another site, through the hub.

Copies of the service answer both their site subject and the `svc.any` subject, so callers that need a specific site can still name it. Use nearest copy when failover matters more than predictability, for example a shared model that should keep answering when the local copy is being updated. Callers should expect latency to vary and read `IEO-Served-By` when they need to know where a call went. The platform does not retry a failed call at another copy; retries are the caller's decision.

### 1.5 Declaring data-plane use

An application declares what it needs in its Application Description, using Margo's specification-extension mechanism. The extension sits on the deployment profile, so the CO copies it unmodified into every ApplicationDeployment, and devices that do not know it ignore it, as Margo requires.

```yaml
deploymentProfiles:
  - type: compose
    id: com-example-line-monitor-compose
    components: [ ... ]
    x-ieo-extensions:
      dataPlane:
        provides:                      # services this app serves
          - service: defect-classifier
            endpoints: [classify]
            exposure: global           # site (default) | global
            discovery: nearest         # explicit (default) | nearest; nearest requires exposure: global
        consumes:                      # services this app calls
          - service: part-detector
            sites: [local]             # local | a site ID | "*" | nearest
        publishes: [detections]        # topics, prefixed data.<site>.<app>. automatically
        subscribes:
          - app: line-monitor
            topic: detections
            sites: ["*"]               # local | a site ID | "*"
        objectStore: readwrite         # none (default) | read | readwrite
```

- `exposure: site` keeps a service reachable only from its own site; `global` lets other sites call it through the hub.
- `discovery: nearest` additionally offers the service on `svc.any.<service>.<endpoint>` (§1.4). It is only valid with `exposure: global`, because a nearest-copy call may be answered at another site.
- A consumer declares `sites: [nearest]` to call `svc.any.<service>.<endpoint>`. This is only allowed for services that are provided with `discovery: nearest`.
- Anything **cross-site** (`exposure: global`, `discovery: nearest`, or `sites` other than `local`) is shown to the operator when the deployment is created and must be approved. The CO records the approval with the deployment.
- A workload with no `dataPlane` declaration gets no data-plane access.

### 1.6 Credentials and connection

- When the LO sends **Apply** for a deployment that declares data-plane use, it issues a NATS user credential in the DATA account whose publish and subscribe permissions are exactly those implied by the declaration and the operator's approval. The credential is bound to the deployment and rotated when the deployment's digest changes.
- The Apply command carries the credential to the EN (over the TLS-protected CONTROL connection). The EN writes it to a file mounted read-only into the deployment's containers and sets:

  | Variable | Value |
  | --- | --- |
  | `IEO_NATS_URL` | The site server's URL |
  | `IEO_NATS_CREDS` | Path of the mounted credentials file |
  | `IEO_SITE_ID` | The site ID |
  | `IEO_HOST_ID` | The host ID |
  | `IEO_DEPLOYMENT_ID` | The deployment ID |
  | `IEO_APP_ID` | The application ID |

- The hub accepts a site's leaf-node link only with that site's credential. Through the hub, a site may publish topic data only under its own prefix (`data.<site>.>`), serve only its own services (`svc.<site>.>`) plus the nearest-copy subjects approved for services it runs (`svc.any.<service>.>`), and reach other sites' services and topics only where an approval in §1.5 allows it. The CO manages the hub's account configuration so that these permissions follow the approvals.
- On Remove, the LO revokes the deployment's credential.

### 1.7 Behavior while disconnected

- **Within a site, everything keeps working** with the hub or the CO unreachable: service calls, topics and the object store are served by the site server.
- **Outgoing topic data** stays in the site stream (up to 24 h or 10 GiB by default) and flows to the hub when the link returns. The hub's aggregated stream then catches up, so cross-site analytics are complete once every site has reconnected.
- **Cross-site service calls** cannot be buffered: they fail immediately with "no responders" or time out. Applications that call other sites must treat those calls as optional or retry later.
- **Nearest-copy calls** keep working offline as long as a copy runs at the caller's site. If none does and the hub is unreachable, they fail with "no responders" like any cross-site call.
- **Placement tip:** models that call each other constantly should be deployed to the same site (and, when latency matters, the same host) using directed deployments or eligibility labels.

### 1.8 Cross-site analytics

Analytics across many sites is an ordinary application deployed through the CO, by default to a central host connected to the hub. It subscribes to the aggregated topic it needs (for example `data.*.line-monitor.detections`) with a durable consumer, so it sees every site's data in order per site, including data buffered during outages. The analytics app itself declares `subscribes` with `sites: ["*"]`, which the operator approves at deployment.

System metrics (CPU, memory, container health) do not use the data plane; they flow through each host's OpenTelemetry collector (overview §10).

## 2. Draft spec (OPTIONAL extension)

The data plane lets workloads call each other and share data within a site and across sites.
Implementations that ship it MUST follow this section; §1 has the full
design.

### 2.1 Topology

- The site NATS server runs a second account, `DATA`, isolated from `CONTROL`.
- A central NATS cluster (the hub) accepts outbound leaf-node links from each site's `DATA` account.
- Traffic between workloads at one site never leaves the site.

### 2.2 Naming

| What | Subject / name |
| --- | --- |
| Service at a named site | `svc.<site>.<service>.<endpoint>` |
| Service, nearest copy | `svc.any.<service>.<endpoint>` |
| Topic | `data.<site>.<app>.<topic>` |
| Site stream | `DATA_<site>` capturing `data.<site>.>` |
| Hub aggregated stream | `DATA_ALL` sourcing every `DATA_<site>` |
| Object store | bucket `obj-<site>`, object `<app>/<key>` |

### 2.3 Declaration and Approval

- An application declares `provides`, `consumes`, `publishes`, `subscribes` and `objectStore`
  under `x-ieo-extensions.dataPlane` on its deployment profile.
- `discovery: nearest` is valid only with `exposure: global`.
- Any cross-site use MUST be approved by the operator when the deployment is created; the CO stores
  the approval with the deployment.
- A service name is unique across the fleet: the CO rejects a second application ID that provides
  the same service name.
- A workload with no declaration gets no data-plane access.

### 2.4 Credentials

- On Apply for a deployment that declares data-plane use, the LO issues a `DATA` account credential
  whose permissions are exactly those implied by the declaration and approval, and adds
  `dataPlane: {url, creds}` to the Command.
- The EN mounts the credential read-only and sets `IEO_NATS_URL`, `IEO_NATS_CREDS`, `IEO_SITE_ID`,
  `IEO_HOST_ID`, `IEO_DEPLOYMENT_ID`, `IEO_APP_ID`.
- The credential rotates when the deployment's digest changes and is revoked on Remove.

### 2.5 Behavior

- Services answer `svc.<site>.<service>.$health` and set reply header `IEO-Served-By: <site>/<host>`;
  errors use reply header `IEO-Error`.
- Topic data is buffered in the site stream (default 24 h or 10 GiB) while the hub link is down.
- Cross-site service calls fail fast when the hub is unreachable. The platform never retries a call
  at another copy.

### 2.6 Extension Conformance

- CONTROL and DATA accounts are isolated; a workload credential cannot reach `site.>` subjects.
- A credential allows exactly the declared and approved subjects.
- Site-local calls and topics keep working with the hub down; topic data reaches `DATA_ALL` after
  reconnect.
- Nearest-copy calls prefer a local copy when one runs (verify with the NATS version in use).
- Credentials rotate on digest change and are revoked on Remove.

