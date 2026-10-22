Intelligent Edge orchestration 
version 0.1.16

### Get Started

download
mv .env.example .env
docker compose up -d
docker ps
edgectl device list


### CO configuration

The CO serves the Margo API to LOs and the operator API to `edgectl` (SPEC §11.3). `co serve` reads
these keys from the environment and exits with code 2 if one is missing or invalid (SPEC §6.1,
§6.3):

| Variable | Key | |
| --- | --- | --- |
| `DATABASE_URL` | | REQUIRED; the Postgres store |
| `CO_MARGO_ADDR` | | default `:9002`; the Margo API |
| `CO_OPERATOR_ADDR` | `co.operator_listen_addr` | default `:8443`; the operator API |
| `CO_OPERATOR_TOKEN` | `co.operator_token` | REQUIRED; at least 32 bytes, `edgectl`'s `EDGECTL_TOKEN` (SPEC §15.6). Environment only |
| `CO_REGISTRY_INSECURE` | `co.registry.insecure` | default `false`; `true` imports from registries over plain HTTP |

The Compose bundle takes `CO_OPERATOR_TOKEN` from its `.env`; the `ieo-co` Helm chart puts
`co.operatorToken` in a Secret and refuses to install without it. `./ieo-co --help` in the container lists the commands.

### edgectl configuration

`edgectl` is the client of the CO's operator API (SPEC §11.3). It reads these keys from the
environment, or from the flag in brackets, which wins (SPEC §6.3), and exits with code 2 if one is
missing or invalid:

| Variable | Flag | Key | |
| --- | --- | --- | --- |
| `EDGECTL_CO_URL` | `--co-url` | `edgectl.co_url` | REQUIRED; the operator API, `https://` |
| `EDGECTL_TOKEN` | none | `edgectl.token` | REQUIRED; the CO's `CO_OPERATOR_TOKEN` (SPEC §15.6). Environment only |
| `EDGECTL_CO_INSECURE` | `--co-insecure` | `edgectl.co_insecure` | `true` allows an `http://` URL; edgectl warns on stderr each time |
| `EDGECTL_TLS_CA_FILE` | `--tls-ca-file` | `edgectl.tls.ca_file` | optional; verifies the CO, instead of the system roots |

`edgectl --help` lists the commands. `--output json` prints the CO's response as it sent it.


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
and `SITE_NATS_PASSWORD` (letters and digits), which the NATS server, the LO and the EN share
(SPEC §15.6). Get the
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
`/var/run/docker.sock`: whoever controls the EN controls that engine. Its data directory is
`/var/lib/ieo/en` on the machine too, so the paths in a deployment's `compose.yaml` find its files. Its workloads run on that same
engine, beside the CO, LO and NATS containers, so a workload that is given the socket could read
their settings; the laptop harness keeps them apart, with an engine per host. The `ieo-lo` Helm
chart has no EN.

### Database schema

The CO creates its `co_*` tables when it starts, and applies any migration the database lacks
(ADR 0013), so neither bundle needs a schema file and an existing volume needs no manual step. The
database role in `DATABASE_URL` needs the rights to create tables in its schema.

A database created by an older bundle also holds the tables of the Git-based CO (`sites`, `hosts`,
`application_descs`, …), which nothing reads any more. They can be dropped by hand; the CO's
tables all start with `co_`.

Developers: see [`docs/contributing.md`](../docs/contributing.md) ("CO store schema") for changing
the schema.

### Restoring the CO database

LOs reject a manifest whose `manifestVersion` is not above the last one they accepted (SPEC §8.2),
so after the CO database is restored from a backup, every site's version must be raised above
anything it published before the restore (SPEC §12). First add again, with `co site add`, any site
added after the backup was taken: the restore lost it, its token and its manifests, and its LO needs
the new token. Likewise run `co site rotate-token` for any site whose token was replaced after the
backup. Then run:

```sh
docker compose exec co ./ieo-co sites raise-versions --by 1000000            # Compose
kubectl exec deploy/<release>-co -- ./ieo-co sites raise-versions --by 1000000   # Helm
```

`--by N` must exceed the number of manifest versions any site may have published since the backup:
each deployment create, update or delete publishes one. A large N costs nothing. The command logs
each site's old and new version, raises retired sites too, and changes nothing else: deployments,
digests and bundles stay as they are. It is safe while the CO is serving. Running it again only
raises the versions further, so rerun it if it stops partway. Until it has run, LOs that poll
reject the restored manifests as rollbacks and keep their current state.
