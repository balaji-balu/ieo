# **CONTRIBUTING.md**

# Contributing to **ieo**

Thank you for your interest in contributing! 🎉
This project is part of the **Edge Orchestration Platform (CO, LO, EN)** ecosystem, and we welcome contributions of all forms — code, documentation, tests, issue reports, and feature suggestions.

---

## Get Started

Run the three services from source against a local Postgres and NATS. All commands run from the
repository root; PowerShell and POSIX shell variants are shown where they differ. To run the whole
stack in containers instead, including two simulated hosts, see [`dev-setup.md`](dev-setup.md).

### 1. Start Postgres and NATS

Start only those two services of the laptop harness:

```sh
docker compose -f deploy/dev/compose.yaml up -d --wait nats postgres
```

This starts Postgres on `5432` (user `postgres`, password `postgres`, database `orchestration`)
and NATS on `4222`.

### 2. Database schema

The CO does not create its tables at startup. Postgres creates them from
`deploy/compose/db/init.sql` (the Atlas migrations in `ent/migrate/migrations/`, concatenated) on
its first start with an empty volume. Without them every site or host registration fails with
`{"error":"db query failed"}`.

**Check:** `docker compose -f deploy/dev/compose.yaml exec postgres psql -U postgres -d orchestration -c "\dt"`
lists `sites` and `hosts`, among others.

**Existing database with an older schema.** If `\dt` shows `site`/`host` but not `sites`/`hosts`,
the volume was created from an older `init.sql`. Either reset it (dev only; deletes all data):

```sh
docker compose -f deploy/dev/compose.yaml down -v
docker compose -f deploy/dev/compose.yaml up -d --wait nats postgres
```

or keep it and apply only the missing migrations with [Atlas](https://atlasgo.io/getting-started),
naming the last migration it already has (the `local` env in `atlas.hcl` points at this Postgres):

```sh
atlas migrate apply --env local --baseline 20251129053632
```

### 3. Configure the services

Each service loads `./.env` from the current directory at startup (missing file is ignored;
variables already set in the shell win). One `.env` in the repository root serves all three:

```dotenv
# --- CO ---
CO_MARGO_ADDR=:9002                # Margo API for LOs and /healthz (default :9002)
CO_PORT=9001                       # the old API, served only when set (until roadmap slice E)
CO_METRICS_PORT=9201
DATABASE_URL=postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable

# --- LO --- (keys: deploy/README.md, "LO configuration")
LO_SITE_ID=00000000-0000-4000-8000-000000000001   # a UUID while the old LO runs (until roadmap slice E)
LO_CO_URL=http://localhost:9002     # the Margo API; no /api/v1 suffix, the LO adds it
LO_CO_INSECURE=true                 # allows http:// on this machine; the LO logs a warning
LO_DATA_DIR=data/lo                 # the LO store is data/lo/lo.db; the old LO uses data/lo/db/bolt.db
LO_SITE_TOKEN=                      # printed by `go run ./cmd/co site add <LO_SITE_ID>` (step 4)
LO_PORT=9010                        # the old LO, run only when set
LO_METRICS_PORT=9202
LO_NATS_URL=nats://localhost:4222
LO_LEGACY_CO_URL=http://localhost:9001   # the old CO API, where the old LO registers

# --- EN ---
IEO_EN_NATS_URL=nats://localhost:4222
IEO_EN_LO_URL=http://localhost:9010

# --- Git-based delivery (being replaced, Appendix B step 2) ---
# GITHUB_TOKEN=                     # read access to the deployments repo; without it the
                                    # CO and LO log Git errors but keep running
```

Give the CO and LO metrics ports different values when both run on one machine.

### 4. Start the services, in order

Each in its own terminal, waiting for the previous one to come up:

The first time, add the LO's site and put the token it prints in `.env` as `LO_SITE_TOKEN`:

```sh
go run ./cmd/co site add 00000000-0000-4000-8000-000000000001
```

Then:

```sh
go run ./cmd/co      # wait for: CO API running on : {"": "9001"} (old API) and "serving the Margo API"
go run ./cmd/lo      # wait for: "outcome":"Accepted" (the Margo sync) and HTTP server started on : {"port": "9010"}
go run ./cmd/en      # expect:   LO {"siteid": "..."}
go run ./cmd/edgectl --help
```

The LO registers its site with the CO once, at startup, and the EN registers with the LO once,
at startup; neither retries. If you start them out of order, restart the later one.

### Troubleshooting

| Symptom | Cause |
| --- | --- |
| EN: `Post "http://localhost:9010/register" … actively refused` | LO not running, or `LO_PORT` unset (the LO then listens on a random port; check its `HTTP server started` line) |
| LO: `invalid configuration` naming a variable | That variable is missing or invalid in `.env` |
| LO: `"outcome":"Unreachable"` with `status 401` | `LO_SITE_TOKEN` is not the site's token; `go run ./cmd/co site rotate-token <LO_SITE_ID>` prints a new one |
| LO: `unsupported protocol scheme ""` (old LO) | `LO_LEGACY_CO_URL` unset |
| LO: `Post "http://localhost:9001/api/v1/register" … actively refused` (old LO) | CO not running; start it, then restart the LO |
| LO: `CO rejected: {"error":"db query failed"}` | Schema missing; see step 2 |
| LO: `CO rejected: {"error":"site already exists"}` | Harmless: the site was registered on an earlier run |
| CO/LO: `clone failed: authentication required: Repository not found` | Git-based delivery without `GITHUB_TOKEN`; harmless for registration |

### Data model changes

Add or change schema files in `ent/schema`, then from the repository root:

```sh
ent generate ./ent/schema --feature sql/upsert
atlas migrate diff <change_name> --env local --to "ent://ent/schema"
atlas migrate apply --env local
```

`atlas.hcl` in the root holds the `local` env. `ent/` is generated: never edit it by hand.

A new migration must also reach the deployment bundles, which create the schema from a single
`init.sql` on first start (see [`deploy/README.md`](../deploy/README.md#database-schema)).
Regenerate it from the migrations and keep the three copies identical:

```powershell
# PowerShell
Get-Content (Get-ChildItem ent\migrate\migrations\*.sql | Sort-Object Name).FullName |
  Set-Content deploy\helm\ieo-co\schemas\init.sql
Copy-Item deploy\helm\ieo-co\schemas\init.sql deploy\compose\db\init.sql
Copy-Item deploy\helm\ieo-co\schemas\init.sql db\init.sql
```

```sh
# POSIX shell
cat ent/migrate/migrations/*.sql > deploy/helm/ieo-co/schemas/init.sql
cp deploy/helm/ieo-co/schemas/init.sql deploy/compose/db/init.sql
cp deploy/helm/ieo-co/schemas/init.sql db/init.sql
```

| File | Used by |
| --- | --- |
| `deploy/helm/ieo-co/schemas/init.sql` | Helm chart `ieo-co` (Postgres init ConfigMap) |
| `deploy/compose/db/init.sql` | `deploy/compose/docker-compose.yaml` |
| `db/init.sql` | `docker-compose-demo.yaml` |

### CO store schema (`internal/co/store/postgres`)

The CO's new store (roadmap B4, ADR 0013) has its own ent schema in
`internal/co/store/postgres/ent/schema`, with `co_*` tables kept apart from the Git-based code's
(ADR 0002). Its versioned SQL migrations live in `internal/co/store/postgres/migrations/` and are
embedded in the binary: the store applies the ones a database lacks when it opens, so no
`init.sql` is involved. To change the schema:

1. Edit `internal/co/store/postgres/ent/schema` and run `go generate ./internal/co/store/postgres/ent`.
2. Add the next migration, e.g. `0002_<change>.sql`, with the SQL that takes the current schema to
   the new one. `go run entgo.io/ent/cmd/ent schema ./internal/co/store/postgres/ent/schema
   --dialect postgres --version 16` prints the full target DDL to start from. Never edit a
   released migration.
3. Run the tests against Postgres: `TestMigrationsMatchSchema` fails, naming the missing SQL, if
   the migrations and the schema disagree.

The store tests run on every backend; set `IEO_TEST_DATABASE_URL` to include Postgres. Each test
creates and drops a schema of its own in that database:

```sh
docker compose -f deploy/dev/compose.yaml up -d --wait postgres
# PowerShell: $env:IEO_TEST_DATABASE_URL = "postgres://..."
export IEO_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable"
go test ./internal/co/...
```

Without it the Postgres subtests skip; CI sets `IEO_REQUIRE_POSTGRES=1`, which makes them fail
instead.

### Inspect the databases

```sh
docker exec -it postgres psql -U postgres -d orchestration    # CO (Postgres)
boltbrowser <BOLTDB_PATH>                                      # LO (bbolt), e.g. C:\ProgramData\lo\db\bolt.db
```

`tests/seeds/site.sql` targets the pre-`20251212095258` `site`/`host` tables and fails against the
current schema. Local runs don't need it: the LO registers its own site.

## 💬 Ways to Contribute

You can help the project in many ways:

* Submitting bug reports
* Proposing new features
* Improving documentation
* Fixing issues
* Writing tests (unit, integration, smoke tests via GitHub Actions)
* Improving deployment flows (Compose/Podman/K8s)

---

## 🧱 Project Structure Overview

```
ieo/
 ├── co/           # Coordinator service
 ├── lo/           # Local orchestrator
 ├── en/           # Edge node runtime
 ├── deployments/  # Sample deployments definition
 ├── configs/      # App configs, YAMLs
 ├── Dockerfile
 ├── podman-compose.yml
 └── README.md
```

---

# 1. 🐛 Reporting Issues

Before opening an issue:

1. **Check existing issues** to avoid duplicates.
2. Provide as much detail as possible:

   * Reproduction steps
   * Logs (CO/LO/EN)
   * Podman/K8s environment
   * Expected vs actual behavior

Template:

```
### Description
<summary of issue>

### Steps to Reproduce
1.
2.
3.

### Expected Behavior

### Actual Behavior

### Logs / Screenshots

### Environment
OS:
Go version:
Container runtime (Podman/Docker):
```

---

# 2. 🔀 Submitting Pull Requests

Follow these steps:

### **1. Fork the repository**

Click **Fork** on GitHub.

### **2. Clone your fork**

```sh
git clone https://github.com/<your-username>/ieo.git
cd ieo
```

### **3. Create a feature branch**

```sh
git checkout -b feature/my-change
```

### **4. Make changes & test locally**

* Run unit tests
* Build CO/LO/EN services
* Smoke test using `podman compose`

Typical commands:

```sh
go test ./...
podman compose up --build
```

### **5. Commit changes**

Follow conventional commit format:

```
feat: add deployment status API
fix: correct LO healthz handler
docs: update configuration examples
refactor: cleanup repo init logic
```

### **6. Push & open PR**

```sh
git push origin feature/my-change
```

Open a pull request using GitHub UI.

### ✔ PR Checklist

* [ ] Code builds locally
* [ ] Tests pass
* [ ] Smoke test passes (`podman compose up`)
* [ ] No console errors
* [ ] Documentation updated (if needed)

---

# 3. 🧪 Tests & CI/CD

This project supports:

### **Unit Tests**

```sh
go test ./...
```

### **Smoke Tests (GitHub Actions)**

Triggered automatically on `push` & `pull_request`.

### **Load Testing (Optional, k6/Hey)**

You may run:

```sh
k6 run tests/load/deploy.js
```

---

# 4. 📦 Coding Guidelines

### **Go Code Standards**

* Use `gofmt` before committing
* Keep files small and readable
* Add comments for exported types & functions
* Avoid unnecessary global state
* Keep CO/LO/EN responsibilities cleanly separated

### **Commit Small, Focused Changes**

Each PR should do one thing.

---

# 5. 🗂 Git Branching Model

We use a lightweight GitOps-friendly model:

| Branch      | Purpose            |
| ----------- | ------------------ |
| `main`      | stable releases    |
| `dev`       | active development |
| `feature/*` | new features       |
| `fix/*`     | bugfixes           |

---

# 6. 🐳 Container & Deployment Rules

If your change affects container images:

* Update **Dockerfile**
* Update **podman-compose.yml**
* Rebuild and test:

  ```sh
  podman compose up --build
  ```

---

# 7. 📄 Documentation Contributions

Documentation lives in:

```
/docs
/README.md
/examples
```

We love improvements to:

* setup guides
* deployment examples
* architecture diagrams
* troubleshooting

---

# 8. 📜 Code of Conduct

Be respectful and constructive.
No abuse, harassment, or toxicity.
Help others learn the system.

---

# 9. ❤️ Thank You!

Your contributions help improve the edge orchestration ecosystem.
If you need any help, open a discussion or ping via issues.

