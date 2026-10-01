# 0010. Margo client API: stay on the pinned rc.3 shape, adopt a pin-change procedure

Status: Proposed · Date: 2026-10-01 · Spec: §3.2, §11.1, §15.2, §15.6, §17.1

## Context
`edge-orchestration-platform/eos-platform` (a sibling prototype) implements a Margo client API on its
CO: `GET /api/v1/onboarding/certificate`, `POST /api/v1/onboarding`, and per-client routes
`/api/v1/clients/{clientId}/{deployments,bundles/{digest},deployments/{deploymentId}/{digest},
deployment/{deploymentId}/status,capabilities}`. It also keeps a conformance discipline: a recorded
upstream pin, an endpoint diff table against it, and an upgrade record for every pin change.

Those routes come from Margo `workload-management-api-1.0.0.yaml` at commit `bc07310`
(2025-12-15). `SPEC.md` pins Margo `1.0.0-rc.3` at commit `f209a7f` (2026-09-16), which changed the
interface between those commits:

| Area | Margo `bc07310` (eos-platform) | Margo `f209a7f` rc.3 (SPEC §11.1) |
| --- | --- | --- |
| Client identity | `{clientId}` path segment | Client certificate SPIFFE ID only; no ID in the path |
| Enrollment | `/api/v1/onboarding`, `/api/v1/onboarding/certificate` | Removed; Trust Bundle API (`/.well-known/margo`, SPIFFE bundle) |
| Request authentication | RFC 9421 HTTP message signatures | Mutual TLS with SPIFFE X.509 SVIDs |
| Capabilities | `POST`/`PUT /clients/{clientId}/capabilities` | `PUT`/`DELETE /capabilities/{deviceId}` (LO and each host) |
| Status | `POST /clients/{clientId}/deployment/{id}/status` | `POST /deployments/{id}/status`, with `adoptedManifestVersion` |
| Errors | Ad hoc | RFC 9457 problem types |

Margo outranks `SPEC.md`, so copying the eos-platform routes would move IEO back to a superseded
Margo version. What is worth taking is the conformance discipline: today `SPEC.md` names a pin
(§ header) and requires tests against "the pinned Margo OpenAPI" (§3.2, §17.1), but says nothing
about where that file lives or how a pin changes.

## Decision
1. The CO ↔ LO interface is the Margo Workload Management API at the pin named in the `SPEC.md`
   header, exactly as §11.1 lists it. IEO does not serve the `bc07310` routes (`/clients/{clientId}/…`,
   `/onboarding…`), not even as aliases.
2. The CO identifies the caller only from its credentials: the per-site bearer token until Appendix
   B step 4 (§15.6, ADR 0005), then the client certificate's SPIFFE ID (§11.1). Never from the path
   or body.
3. The pinned upstream OpenAPI file is vendored unchanged at
   `api/margo/<commit>/workload-management-api-<version>.yaml`, with a `README.md` stating the
   upstream repository, commit SHA and source path. §17.1 contract tests validate Margo request and
   response bodies against this file. Types in `internal/contract` stay hand-written (roadmap
   slice A); they are tested against the file, not generated from it.
4. A pin change is its own PR and contains:
   - the prior and new commit SHA, and the `SPEC.md` header updated to the new one;
   - a short summary of upstream changes to the management interface between the two commits;
   - an endpoint diff table (method, path, status `Match | Different | Missing | Local-Only`,
     note) appended to `docs/margo-pins.md`;
   - a compatibility class: compatible, adapter needed, or breaking;
   - the `SPEC.md` sections updated to match, the new OpenAPI file vendored, and §17.1 green.
   A row other than `Match` needs a reason, or the PR fixes it.
5. The WM API surface has no `Local-Only` endpoints. IEO-specific operations go on the operator API
   (§11.3).
6. Enrollment under rc.3 (Trust Bundle API, SVID issuance) is decided in the ADR for Appendix B
   step 4, not here. Until then §15.2 and ADR 0005 apply.

## Consequences
- IEO stays on the newest Margo shape, and there is one CO ↔ LO surface to test.
- eos-platform code under `/api/v1/clients/…` cannot be ported as is; only its ideas (diff table,
  upgrade record) carry over.
- `SPEC.md` names the vendored file location in its header, and §17.1 states the item 4 rule as
  prose rather than a bullet, so the conformance tool does not expect a `TestSpec_` test for it.
  `docs/margo-pins.md` records the `f209a7f` baseline. Roadmap slice A vendors the file.
- Margo is still pre-draft, so pin changes will be frequent; item 4 makes each one reviewable in
  about the 15 minutes a roadmap PR is meant to take.
- Watch: if hand-written types and the vendored file drift often, revisit generation (option C).

## Options considered
- A. Adopt eos-platform's `/clients/{clientId}` and onboarding routes — they follow Margo
  `bc07310`, which rc.3 superseded; Margo outranks `SPEC.md`.
- B. Serve both shapes during a transition — no Margo client uses the old shape against IEO, and it
  doubles the surface that §15 security review has to cover.
- C. Generate server and client types from the vendored OpenAPI (e.g. `oapi-codegen`) — adds a
  dependency and generated code before slice A has shown hand-written types are a problem.
- D. Keep the pin in the `SPEC.md` header only, with no vendored file or procedure (status quo) —
  §17.1 cannot run offline against a fixed file, and pin changes have no review checklist.
