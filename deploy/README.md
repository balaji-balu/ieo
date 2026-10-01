Intelligent Edge orchestration 
version 0.1.16

### Get Started

download
mv .env.example .env
docker compose up -d
docker ps
edgectl status


### Database schema

The CO does not create its tables at startup. Both bundles create them from a single SQL file
that Postgres runs on its **first start with an empty data volume**:

| Bundle | Schema file | Mounted at |
| --- | --- | --- |
| Compose (`deploy/compose`) | `db/init.sql` | `/docker-entrypoint-initdb.d` (bind mount `./db`) |
| Helm (`ieo-co` chart) | `schemas/init.sql` | `/docker-entrypoint-initdb.d` (ConfigMap `<release>-postgres-schema`) |

Each file is the Atlas migrations in `ent/migrate/migrations/` concatenated in name order (latest
included: `20251212095258`). Check after the first start that the list includes `sites` and `hosts`:

```sh
docker compose exec postgres psql -U $PG_USER -d $PG_DB -c "\dt"       # Compose
kubectl exec deploy/<release>-postgres -- psql -U <user> -d <db> -c "\dt"   # Helm
```

If `sites` and `hosts` are missing, every LO and EN registration fails with
`{"error":"db query failed"}`.

**Existing volume.** Postgres skips the init file when the data volume already holds a database,
so a newer `init.sql` never reaches an existing deployment. Apply the missing migrations with
[Atlas](https://atlasgo.io/getting-started) from a checkout of the release you're upgrading to,
naming the last migration the volume already has as `--baseline`:

```sh
atlas migrate apply \
  --url "postgres://$PG_USER:$PG_PASSWORD@<postgres-host>:5432/$PG_DB?sslmode=disable" \
  --dir file://ent/migrate/migrations \
  --baseline <last applied version>
```

Volumes created by the Compose bundle before `init.sql` included `20251212095258` have only
`20251129053632`; use that as the baseline. `--baseline` is needed only on the first Atlas run
against a volume; Atlas records revisions from then on.

For a test install you can instead delete the volume and start again (deletes all data):
`docker compose down -v`, then `docker compose up -d`. For Helm, delete the
`<release>-postgres-pvc` PersistentVolumeClaim before reinstalling.

Developers: see [`docs/contributing.md`](../docs/contributing.md#data-model-changes) for keeping
the schema files in step with new migrations.
