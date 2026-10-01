---
artifact_id: INT-006
issue:
title: Condition monitoring (gateway tier)
repo: ieo-examples (apps/condition-monitoring/)
status: draft
priority: P2
depends_on: [INT-003]
affected_spec: ["§5", "§5.4", "§8.6", "App. A", "proposed device-class rule"]
human_checkpoint: owner agrees it is worth doing
---

# INT-006: Condition monitoring (gateway tier)

## Problem
IEO claims to be suitable for gateways, but no example app is sized for one. The typical
industrial gateway workload (collect sensor data, analyse locally, alert on anomalies) is not
demonstrated, so footprint and ARM support are unproven.

## Outcome
A small condition-monitoring app that reads vibration and temperature data from the INT-003
simulators, computes simple health indicators on the gateway, raises alerts when thresholds or
trends are crossed, and keeps working when the site is cut off from the centre.

## Acceptance criteria
- Runs within a gateway budget: total under 256 MB RAM on the BeagleBoard.
- Eligibility rules place it on gateway-class hosts only.
- Thresholds and sensor endpoints are Margo parameters.
- An injected sensor fault (INT-003) produces an alert within a configurable time.
- Alerts raised while offline are delivered after reconnection, if the data plane is used.

## Success metrics
- Deployed, updated and removed on the BeagleBoard through `edgectl` with no manual steps.

## Non-goals
- Machine-learning anomaly detection; simple rules are enough.
- Dashboards beyond a minimal status view.

## Open questions
- Use Appendix A topics for alerts now, or plain MQTT until the data plane exists?
