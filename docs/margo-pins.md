# Margo pin record

Every change of the Margo commit that `SPEC.md` pins is recorded here, newest first (SPEC §17.1,
ADR 0010). The pinned OpenAPI file lives in `api/margo/<commit>/`.

## f209a7f — Workload Management API 1.0.0-rc.3 (baseline)

- Date: 2026-10-01
- Upstream: `margo/specification` commit `f209a7f21304a906bf637db044a293f7b0b1e586`,
  `system-design/specification/margo-management-interface/workload-management-api-1.0.0-rc.3.yaml`
- Prior pin: none (first recorded pin)
- Compatibility: n/a
- Vendored file: `api/margo/f209a7f/workload-management-api-1.0.0-rc.3.yaml` (roadmap slice A)
- Known upstream defect: `UnsignedAppStateManifest.required` names `bundle.mediaType`,
  `bundle.digest` and `bundle.url`, which no valid manifest can satisfy. The §17.1 tests drop those
  entries in memory; see `api/margo/f209a7f/README.md`.
- Added files, same commit (roadmap slice B1): `application-description.linkml.yaml`, copied from
  `src/specification/applications/`, and `application-description.schema.json`, generated from it
  with `linkml` 1.11.1. The CO validates Application Descriptions against the generated schema
  (§5.3), with two generator defects worked around in memory; see the README there.

| Method | Path | Status | Note |
| --- | --- | --- | --- |
| PUT | `/api/v1/capabilities/{deviceId}` | Match | §11.1, §8.3 |
| DELETE | `/api/v1/capabilities/{deviceId}` | Match | §11.1, §8.3 |
| GET | `/api/v1/deployments` | Match | §11.1, §8.2; ETag and `304` |
| GET | `/api/v1/deployments/{deploymentId}/{digest}` | Match | §11.1, §8.2 |
| GET | `/api/v1/bundles/{digest}` | Match | §11.1, §8.2 |
| POST | `/api/v1/deployments/{deploymentId}/status` | Match | §11.1, §8.8 |
