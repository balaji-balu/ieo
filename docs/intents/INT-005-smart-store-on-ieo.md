---
artifact_id: INT-005
issue:
title: Smart Store deployed on IEO
repo: smart-store
status: draft
priority: P3
depends_on: [INT-001, INT-004]
affected_spec: ["§5", "§5.4", "§8.6", "§14.2", "§15.4", "App. A"]
links: ["Smart Store spec (Obsidian vault)", "anthropics/commerce-agents"]
human_checkpoint: owner confirms against the Smart Store spec before Spec stage
---

# INT-005: Smart Store deployed on IEO

## Problem
IEO has no realistic, multi-tier application to prove it can run a business workload across
device classes. Smart Store needs a way to be delivered to many stores, kept running while a
store is offline, and updated centrally. Neither project has shown the other works.

## Outcome
Smart Store published as a Margo application package and run on the INT-001 testbed, with each part
on the tier that suits it:

| Tier | Smart Store part |
|---|---|
| IoT (simulated, INT-003) | Shelf scales, people counter |
| Gateway | Sensor adapter, MQTT broker |
| Edge server | Storefront API with shopping agent, storefront web, store database |
| Central / HQ | LLM gateway holding the API key; catalog and price master |

The shopping agent follows the commerce-agents blueprint (a `StorefrontBackend` over the store's
local data).

## Acceptance criteria
- Smart Store installs on a testbed site from the registry through `edgectl`, with no manual
  steps on the hosts.
- Store configuration (store ID, brand, enabled features, gateway URL) is set through Margo
  parameters (§5.4).
- No API key or other secret is passed as a parameter or baked into an image (§15.4).
- With the central link down, catalog search, cart and checkout keep working from local data;
  the chat assistant reports itself unavailable instead of failing.
- Carts and sessions survive an application version update.
- Live stock from simulated shelf sensors is visible in product answers.

## Success metrics
- A version update rolls out to both sites with no lost carts.
- A full day of simulated store traffic runs with the central link cut for part of it.

## Non-goals
- Payments, real POS integration, or production security.
- Running the language model at the edge.

## Assumptions
- Smart Store is a physical-retail product. This must be checked against its spec.

## Open questions
- How does the LLM gateway authenticate a store (site certificate, token)?
- Does the merchant (store manager) agent belong in this intent or a later one?
