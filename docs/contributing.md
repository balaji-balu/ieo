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

The CO creates its `co_*` tables and applies any migration the database lacks when it starts
(ADR 0013; "CO store schema" below). There is nothing to run by hand.

### 3. Configure the services

Each service loads `./.env` from the current directory at startup (missing file is ignored;
variables already set in the shell win). One `.env` in the repository root serves all three:

```dotenv
# --- CO ---
CO_MARGO_ADDR=:9002                # Margo API for LOs and /healthz (default :9002)
CO_OPERATOR_ADDR=:8443             # operator API for edgectl (default :8443)
CO_OPERATOR_TOKEN=                 # at least 32 bytes, made up per machine (docs/dev-setup.md, Start)
DATABASE_URL=postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable

# --- LO --- (keys: deploy/README.md, "LO configuration")
LO_SITE_ID=00000000-0000-4000-8000-000000000001   # the harness site ID
LO_CO_URL=http://localhost:9002     # the Margo API; no /api/v1 suffix, the LO adds it
LO_CO_INSECURE=true                 # allows http:// on this machine; the LO logs a warning
LO_DATA_DIR=data/lo                 # the LO store is data/lo/lo.db
LO_SITE_TOKEN=                      # printed by `go run ./cmd/co site add <LO_SITE_ID>` (step 4)
LO_NATS_URL=nats://localhost:4222   # the site NATS server
LO_NATS_INSECURE=true               # allows nats:// on this machine; the LO logs a warning
LO_NATS_USERNAME=site               # the site's NATS login (SPEC §15.6): the harness server's,
LO_NATS_PASSWORD=                   #   SITE_NATS_PASSWORD in deploy/dev/.env

# --- EN --- (keys: deploy/README.md, "EN configuration")
EN_SITE_ID=00000000-0000-4000-8000-000000000001   # LO_SITE_ID
EN_HOST_ID=host-1
EN_DATA_DIR=data/en                 # host.id and the EN store, state.db
EN_NATS_URL=nats://localhost:4222
EN_NATS_INSECURE=true
EN_NATS_USERNAME=site
EN_NATS_PASSWORD=                   # the same as LO_NATS_PASSWORD
EN_OTEL_HTTP_ENDPOINT=http://localhost:4318   # required; nothing listens there until roadmap slice M
EN_REGISTRY_INSECURE=true           # the harness registry, localhost:5000, is plain HTTP
```

### 4. Start the services, in order

Each in its own terminal, waiting for the previous one to come up:

The first time, add the LO's site and put the token it prints in `.env` as `LO_SITE_TOKEN`:

```sh
go run ./cmd/co site add 00000000-0000-4000-8000-000000000001
```

Then:

```sh
go run ./cmd/co      # wait for: "serving the Margo API" and "serving the operator API"
go run ./cmd/lo      # wait for: "outcome":"Accepted" (the Margo sync)
go run ./cmd/en      # wait for: "taking commands"; it needs `docker compose` on PATH
go run ./cmd/edgectl --help
```

The LO and the EN keep trying to reach the site NATS server, and the LO keeps polling the CO, so
they can start in any order. One `.env` holds the keys of all three, as their prefixes differ.

### Troubleshooting

| Symptom | Cause |
| --- | --- |
| EN: exit 1, `the Compose CLI` in its last line | `docker compose version` does not run; install Docker with the Compose plugin |
| LO or EN: `cannot connect to the site NATS server` | NATS not running, or the password is not the server's `SITE_NATS_PASSWORD` |
| LO: `invalid configuration` naming a variable | That variable is missing or invalid in `.env` |
| LO: `"outcome":"Unreachable"` with `status 401` | `LO_SITE_TOKEN` is not the site's token; `go run ./cmd/co site rotate-token <LO_SITE_ID>` prints a new one |

### CO store schema (`internal/co/store/postgres`)

The CO's new store (roadmap B4, ADR 0013) has its own ent schema in
`internal/co/store/postgres/ent/schema`, with `co_*` tables. Its versioned SQL migrations live in
`internal/co/store/postgres/migrations/` and are embedded in the binary: the store applies the ones a database lacks when it opens, so no
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

