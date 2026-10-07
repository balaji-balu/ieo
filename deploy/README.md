Intelligent Edge orchestration 
version 0.1.16

### Get Started

download
mv .env.example .env
docker compose up -d
docker ps
edgectl status


### LO configuration

The LO syncs with the CO's Margo API (SPEC §8.2). It reads these keys from the environment, or
from the flag in brackets, which wins (SPEC §6.1, §6.3), and exits with code 2 if one is missing
or invalid:

| Variable | Flag | Key | |
| --- | --- | --- | --- |
| `LO_SITE_ID` | `--site-id` | `lo.site_id` | REQUIRED |
| `LO_CO_URL` | `--co-url` | `lo.co_url` | REQUIRED; the CO's Margo API, `https://` |
| `LO_SITE_TOKEN` | none | `lo.site_token` | REQUIRED; from `co site add` (SPEC §15.6). Environment only |
| `LO_DATA_DIR` | `--data-dir` | `lo.data_dir` | REQUIRED; the store is `lo.db` in it |
| `LO_TLS_CA_FILE` | `--tls-ca-file` | `lo.tls.ca_file` | optional; verifies the CO, instead of the system roots |
| `LO_CO_INSECURE` | `--co-insecure` | `lo.co_insecure` | default `false`; `true` allows an `http://` `LO_CO_URL` |
| `LO_POLL_INTERVAL` | `--poll-interval` | `lo.poll.interval` | default `60s` |

The LO uses no HTTP proxy for the CO (SPEC §15.6). The old LO runs beside it while `LO_PORT` is
set; it registers with the old CO API at `LO_LEGACY_CO_URL` (until roadmap slice E).

The Compose bundle (`deploy/compose`) and the `ieo-lo` Helm chart do not set these keys yet, so
an LO image built from current code exits at startup with them. `deploy/dev/compose.yaml` does
(see `docs/dev-setup.md`).

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

### Restoring the CO database

LOs reject a manifest whose `manifestVersion` is not above the last one they accepted (SPEC §8.2),
so after the CO database is restored from a backup, every site's version must be raised above
anything it published before the restore (SPEC §12). First add again, with `co site add`, any site
added after the backup was taken: the restore lost it, its token and its manifests, and its LO needs
the new token. Likewise run `co site rotate-token` for any site whose token was replaced after the
backup. Then run:

```sh
docker compose exec co /app/orchestrator sites raise-versions --by 1000000            # Compose
kubectl exec deploy/<release>-co -- /app/orchestrator sites raise-versions --by 1000000   # Helm
```

`--by N` must exceed the number of manifest versions any site may have published since the backup:
each deployment create, update or delete publishes one. A large N costs nothing. The command logs
each site's old and new version, raises retired sites too, and changes nothing else: deployments,
digests and bundles stay as they are. It is safe while the CO is serving. Running it again only
raises the versions further, so rerun it if it stops partway. Until it has run, LOs that poll
reject the restored manifests as rollbacks and keep their current state.
