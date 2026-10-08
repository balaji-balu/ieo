# 0016. EN store: host ID file and a bbolt store for applied deployments

Status: Proposed · Date: 2026-10-08 · Spec: §4.1.2, §4.1.12, §9.1, §12, §14.2

## Context
§9.1 puts the EN's host ID in `<en.data_dir>/host.id` and its embedded store in
`<en.data_dir>/state.db`. §4.1.12 and §12 say what the store holds (`applied`, `component_states`)
and that `applied` changes after each Apply or Remove. The layout and the handling of a damaged
store are implementation-defined. Roadmap slice D (balaji-balu/ieo#58) builds the EN, and its
store is a lasting format, as the LO's is (ADR 0014).

If the EN loses `applied`, its inventory reports nothing. The LO then re-Applies what it wants, so
wanted deployments recover. But it never sends Remove for a deployment the EN no longer reports,
so that deployment's containers keep running unmanaged. A damaged store must therefore stop the EN,
not reset it.

## Decision
- `<en.data_dir>` is created with mode `0700` if it does not exist; an existing directory's mode is
  left as it is (ADR 0014; on Windows the directory's ACL governs).
- `host.id` holds the host ID (§4.2) and a newline, mode `0600`.
  - No file and no `en.host_id`: the EN generates an ID (a lowercase UUID), writes the file, and
    syncs it before connecting to NATS.
  - No file and `en.host_id` set: the EN writes the configured ID.
  - A file whose ID differs from a set `en.host_id`, or that is not a valid host ID, is a
    configuration error: the EN exits at startup naming the file (§6.1). Identity never changes
    without the operator removing the file (§6.2).
- `state.db` is a bbolt file opened with mode `0600` and a 1 s lock timeout, so a second EN on the
  same directory fails at startup. Layout version 1:

  | Bucket | Key | Value |
  | --- | --- | --- |
  | `meta` | `schema` | layout version, 8 bytes big-endian |
  | `applied` | deployment ID (lowercase UUID) | JSON `{"digest", "composeProjects"}`; `composeProjects` an array of project names (§4.2) |
  | `component_states` | deployment ID (nested bucket) | in the nested bucket: component name → JSON `{"name", "state", "error"}` (§4.1.9; `error` optional) |

- An empty file gets every bucket above. A file with buckets but no `meta`, or another `schema`, is
  refused at open. A record that does not decode, or a `component_states` entry with no `applied`
  entry, fails `Load`. Either way the EN exits at startup with an error naming the file and never
  deletes or overwrites it. Recovery is an operator action: bring down the host's IEO Compose
  projects, then remove the file.
- The Apply that records `applied` (§8.9 step 6) also writes that deployment's `component_states` in
  the same transaction. Remove deletes both in one transaction. Each status the EN publishes is
  written to `component_states` before it is published.
- A later change to a bucket's encoding raises `schema` and migrates in the open function, recorded
  in an ADR that refines this one.

## Consequences
- After an EN restart, inventory starts from what the store recorded. Roadmap L then refreshes it
  from the runtime (§16.7), so inventory reflects the containers actually running (§17.6).
- A damaged store takes the host out of service until the operator acts, rather than leaving
  unmanaged containers behind.
- Component names are bucket keys, so any name Margo allows works, including one with `/`.

## Options considered
- Treat a damaged store as empty: simpler, but it orphans deployments the LO no longer wants.
- Rebuild `applied` from Compose project labels on the engine: possible, since project names derive
  from the deployment ID (§4.2), but the digest is not on the engine. This belongs to roadmap L's
  refresh from the runtime, not to the store.
- Keep the host ID inside `state.db`: §9.1 names a separate file, and a separate file lets an
  operator reset the store without changing the host's identity.
