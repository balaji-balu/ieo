lo.start (the old LO, run from cmd/lo/legacy.go while LO_PORT is set)
    nats : subscribe health receive from en 
        - host lifecycle. health monitoring. receives health from en. heartbeat monitor. 
    nats : subscribe status receive from en 
        - update the actual hash when status is success

The Git watcher, its push/pull/offline mode handlers and the gitpolled event are gone (roadmap
C3b, ADR 0002). Desired state now comes from the Margo sync loop in internal/lo/sync; reconciling
it onto ENs is roadmap slice D.

Schema: 
```
siteinfo/
    site-id → { site metadata }

desired/
    site-id/
        app-id/
            version
            components/
                comp-name → { version, content }

hosts/
    host-id → { Alive: true/false }

actual/
    site-id/
        host-id/
            app-id/
                version
                components/
                    comp-name → { status, last_updated, hash }

ops/
    site-id/
        host-id/
            op-uuid → { action, app, comp, version, timestamp }

site_state/
    site-id → { last_desired_sync, last_actual_sync }


```
| Scenario                     | Condition                                | Action                        |
| ---------------------------- | ---------------------------------------- | ----------------------------- |
| App version differs          | desired vs actual                        | Full app update               |
| Component version differs    | app version same, component hash differs | Component-level update op     |
| App removed                  | exists in actual, not in desired         | Remove app and all components |
| Component removed            | exists in actual, not in desired         | Remove component              |
| App added                    | exists in desired, not in actual         | Install app + all components  |
| Component added              | exists in desired, not in actual         | Install component             |
| App and components identical | versions & hashes match                  | Do nothing                    |

```