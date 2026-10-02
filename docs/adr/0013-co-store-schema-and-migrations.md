# 0013. CO store: own ent schema, embedded SQL migrations applied on open

Status: Proposed · Date: 2026-10-02 · Spec: §12 · Refines: 0004 (migrations part)

## Context
ADR 0004 keeps ent on Postgres with Atlas versioned migrations, created with the Atlas CLI and
shipped to Postgres as one concatenated `init.sql` that runs only on a new, empty volume. Roadmap B4
adds the CO store for the new code (§12). Three things don't fit that routine:

- The existing `ent/schema` belongs to the Git-based code and already has `sites`, `hosts` and
  `deployment_status` tables; ADR 0002 keeps new code off the old packages.
- A database created before a new migration never gets it from `init.sql`; every schema change
  needs a manual Atlas step on every laptop, Compose and Helm install.
- The Atlas CLI needs Docker for its dev database, and isn't installed in cloud sessions or CI.

## Decision
- The CO store has its own ent schema in `internal/co/store/postgres/ent/schema`, generated with
  `go generate ./internal/co/store/postgres/ent`. Its tables, indexes and constraints are prefixed
  `co_`.
- Its migrations are versioned SQL files in `internal/co/store/postgres/migrations/`, named
  `NNNN_<change>.sql`, embedded in the binary. A released migration is never edited.
- `postgres.Open` applies, in one transaction under an advisory lock, every migration the database
  lacks, recorded in `co_schema_migrations`. No `init.sql` is involved, and several COs may start at
  once.
- A migration is written by hand. `go run entgo.io/ent/cmd/ent schema … --dialect postgres` prints
  the full target DDL to start from.
- `TestMigrationsMatchSchema` applies the migrations and asks ent for the difference to the schema.
  It fails, printing the missing SQL, unless there is none. CI runs it against Postgres.
- Constraints ent can't express (the §12 `manifest_version` trigger) live only in migrations.
- `contributing.md` ("CO store schema") describes the steps.

## Consequences
- An existing database gets new migrations when the CO next starts; no manual step.
- No Atlas CLI or Docker is needed to change the schema, on Windows or in a cloud session.
- Writing the SQL is a manual step, with the drift test as the safety net. Atlas's migration
  linting (destructive change checks) is lost; a reviewer checks a migration that drops or rewrites
  data.
- The database role the CO uses needs DDL rights on its schema.
- Two ent trees and two migration styles live in the repo until the Git-based code is deleted
  (roadmap, after slice E); then `ent/`, `db/` and `atlas.hcl` go with it.

## Options considered
- Atlas CLI for the new schema too (ADR 0004 as written) — needs Docker for the dev database and a
  manual apply on every existing database; `init.sql` covers only new volumes.
- Atlas as a library at startup (`ariga.io/atlas` migrate executor) — a large dependency for applying
  a few SQL files in order.
- `golang-migrate` — a new dependency and a second migration-table convention, for what about 60
  lines of code do here.
- ent auto-migration (`client.Schema.Create`) at startup — not versioned, can't hold the trigger,
  and changes a production schema without review.
