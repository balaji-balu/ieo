---
artifact_id: INT-003
issue:
title: Device simulators
repo: ieo-examples (simulators/)
status: draft
priority: P2
depends_on: []
affected_spec: ["§4.1.3 (peripherals)", "App. A"]
human_checkpoint: owner agrees it is worth doing
---

# INT-003: Device simulators

## Problem
Industrial and retail apps (INT-005 to INT-008) consume data from devices IEO does not manage: sensors,
PLCs, cameras, shelf scales. Without that hardware, the apps cannot run realistically on the
testbed, and their behavior under device faults cannot be tested.

## Outcome
A set of small, configurable device simulators, each published as a multi-arch container image,
that apps on the testbed connect to as if they were real devices:

| Simulator | Protocol | Emits | Used by |
|---|---|---|---|
| Vibration / temperature sensor | MQTT | Time series with configurable noise, drift and fault spikes | INT-006 |
| PLC | Modbus TCP | Machine state, cycle counts, reject counts | INT-008 |
| Process data server | OPC UA | Tags for a small production line | INT-006, INT-008 |
| Camera feed | RTSP or HTTP frames | A looping image set with known defects | INT-007 |
| Shelf scale / people counter | MQTT | Weight changes and footfall | INT-005 |

## Acceptance criteria
- Each simulator starts from one command with a config file. Values, rates and fault patterns
  are configurable without rebuilding.
- Each simulator can inject a device fault on command: stop emitting, emit garbage, drift.
- Images exist for amd64 and the BeagleBoard's architecture.
- Each simulator's README states its protocol, data model and how to point an app at it.
- Simulators can run outside IEO (on the laptop) or as IEO deployments themselves.

## Success metrics
- INT-006, INT-007 and INT-008 run end to end on the testbed with no physical devices.

## Non-goals
- Protocol conformance testing of the simulators.
- Real device firmware, provisioning or OTA updates.

## Open questions
- Should simulators also be deployed through IEO (dogfooding), or always run beside it?
