# 0005. Interim token auth until mTLS

Status: Accepted · Date: 2026-09-30 · Spec: §15.6, Appendix B step 4 · Overview open question 2

## Context
Mutual TLS with SPIFFE IDs is Appendix B step 4. Until then every endpoint would be open, including
host registration and status reporting.

## Decision
From the first slice that adds each interface `[IEO, interim]`:
- CO ↔ LO: a per-site bearer token issued at site registration; the CO maps token → site and serves
  only that site's resources.
- LO ↔ EN: per-site NATS username/password; ENs may publish only under `site.<site_id>.host.<host_id>.>`
  once scoped credentials land, shared per-site credentials before that.
- `edgectl` → CO: one static operator token from config.
- Tokens come from config or environment, never from code, logs or fixtures.
- Removed when Appendix B step 4 lands; `SPEC.md` §15.6 states these rules as `[IEO, interim]`.

## Consequences
Laptop and demo deployments are not wide open. Token handling is simple enough to replace later.
