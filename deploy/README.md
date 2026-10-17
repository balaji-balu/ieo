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
| `LO_NATS_URL` | `--nats-url` | `lo.nats.url` | REQUIRED; the site NATS server, `tls://host[:port]` |
| `LO_NATS_USERNAME` | `--nats-username` | `lo.nats.username` | REQUIRED; the site's NATS user (SPEC §15.6) |
| `LO_NATS_PASSWORD` | none | `lo.nats.password` | REQUIRED; environment only |
| `LO_NATS_CA_FILE` | `--nats-ca-file` | `lo.nats.ca_file` | optional; verifies the NATS server, instead of the system roots |
| `LO_NATS_INSECURE` | `--nats-insecure` | `lo.nats_insecure` | default `false`; `true` allows a `nats://` `LO_NATS_URL`, which does not require TLS |
| `LO_COMMAND_ACK_TIMEOUT` | `--command-ack-timeout` | `lo.command.ack_timeout` | default `5s`; how long a host has to acknowledge a command |
| `LO_RECONCILE_INTERVAL` | `--reconcile-interval` | `lo.reconcile.interval` | default `5m`; the safety pass over all hosts |

The LO uses no HTTP proxy for the CO (SPEC §15.6). A NATS server it cannot reach, or that refuses
its login, does not stop it: it logs a warning, keeps trying, and goes on syncing with the CO
(SPEC §11.2). A site ID that contains `.` is refused at startup, since it goes into every NATS
subject (SPEC §4.2). The LO serves no HTTP port.

The Compose bundle (`deploy/compose`) sets these keys from its `.env`: `SITE_ID`, `LO_SITE_TOKEN`
and `SITE_NATS_PASSWORD`, which the NATS server, the LO and the EN share (SPEC §15.6). Get the
token with `docker compose exec co ./ieo-co site add <SITE_ID>`. The `ieo-lo` Helm chart takes the
site ID from `site.id`, and the token and the NATS password from a Secret it does not create (see
its `values.yaml`). `deploy/dev/compose.yaml` is the laptop harness (see `docs/dev-setup.md`).

### EN configuration

The EN runs on each host, beside its container engine, and takes the LO's commands over the site
NATS server (SPEC §8.9). It reads these keys from the environment, or from the flag in brackets,
which wins (SPEC §6.1, §6.3). It exits with code 2 if one is missing or invalid, and 1 if it
cannot start, for example when `docker compose version` does not run (ADR 0003):

| Variable | Flag | Key | |
| --- | --- | --- | --- |
| `EN_SITE_ID` | `--site-id` | `en.site_id` | REQUIRED |
| `EN_HOST_ID` | `--host-id` | `en.host_id` | optional; kept in `host.id` in the data directory, generated on first start if unset (ADR 0016) |
| `EN_DATA_DIR` | `--data-dir` | `en.data_dir` | REQUIRED; the store is `state.db` in it |
| `EN_RUNTIME` | `--runtime` | `en.runtime` | `docker` (default) or `podman` |
| `EN_NATS_URL` | `--nats-url` | `en.nats_url` | REQUIRED; the site NATS server, `tls://host[:port]` |
| `EN_NATS_USERNAME` | `--nats-username` | `en.nats.username` | REQUIRED; the site's NATS user (SPEC §15.6) |
| `EN_NATS_PASSWORD` | none | `en.nats.password` | REQUIRED; environment only |
| `EN_NATS_CA_FILE` | `--nats-ca-file` | `en.nats.ca_file` | optional; verifies the NATS server, instead of the system roots |
| `EN_NATS_INSECURE` | `--nats-insecure` | `en.nats_insecure` | default `false`; `true` allows a `nats://` `EN_NATS_URL`, which does not require TLS |
| `EN_INVENTORY_INTERVAL` | `--inventory-interval` | `en.inventory.interval` | default `60s` |
| `EN_OTEL_HTTP_ENDPOINT` | `--otel-http-endpoint` | `en.otel.http_endpoint` | REQUIRED; given to workloads as `HTTP_OTEL_EXPORTER_OTLP_ENDPOINT` (SPEC §9.3) |
| `EN_OTEL_GRPC_ENDPOINT` | `--otel-grpc-endpoint` | `en.otel.grpc_endpoint` | optional; `GRPC_OTEL_EXPORTER_OTLP_ENDPOINT` |
| `EN_REGISTRY_AUTH_FILE` | `--registry-auth-file` | `en.registry.auth_file` | optional; a Docker `config.json` of which only `auths` is read (ADR 0021) |
| `EN_REGISTRY_INSECURE` | `--registry-insecure` | `en.registry.insecure` | default `false`; `true` reaches every registry over plain HTTP |
| `EN_PULL_MAX_BYTES` | `--pull-max-bytes` | `en.pull.max_bytes` | default `268435456` (256 MiB) |
| `EN_PULL_TIMEOUT` | `--pull-timeout` | `en.pull.timeout` | default `5m` |
| `EN_ARCHIVE_MAX_EXTRACTED_BYTES` | `--archive-max-extracted-bytes` | `en.archive.max_extracted_bytes` | default `1073741824` (1 GiB) |
| `EN_ARCHIVE_MAX_ENTRIES` | `--archive-max-entries` | `en.archive.max_entries` | default `10000` |
| `EN_START_TIMEOUT` | `--start-timeout` | `en.start_timeout` | default `5m` |

The Compose bundle runs one EN with the engine of the machine it runs on, through
`/var/run/docker.sock`: whoever controls the EN controls that engine. The `ieo-lo` Helm chart has no
EN.

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
