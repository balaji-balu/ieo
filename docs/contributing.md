# **CONTRIBUTING.md**

# Contributing to **ieo**

Thank you for your interest in contributing! 🎉
This project is part of the **Edge Orchestration Platform (CO, LO, EN)** ecosystem, and we welcome contributions of all forms — code, documentation, tests, issue reports, and feature suggestions.

---

## Get Started

// setup nats, postgres
podman-compose -f docker-compose.dev.yaml up -d

In separate terminal, start co
`go run ./cmd/co` 

In separate terminal, start lo
`go run ./cmd/lo` 

In separate terminal, start en
`go run ./cmd/en`

run cli
`go run ./cmd/edgectl`

#### Configuration
```
GITHUB_TOKEN=
DATABASE_URL=
SITE_ID  = 
NODE_ID  =
RUNTIME = containerd(default), wasm, compose_pkg, helm
DEPLOYMENTS_REPO = https://github.com/edge-orchestration-platform/deployments (this is where co writes deployment requests. lo will monitor for this repo changes for its site)
APPLICATIONS_REPO = https://github.com/edge-orchestration-platform/app-registry (this is for testing. actual repo will be on developers site)
AI_SAMPLE_DEMO = ghcr.io/edge-orchestration-platform/edge-ai-sample(sample edge ai)
```
##### Data Models addition/modifications

go to root directory
add schema files to `ent/schema` and then 

```
ent generate ./ent/schema  --feature sql/upsert
atlas migrate diff add_deloymentstatus --env local --to "ent://ent/schema"
atlas migrate apply --env local
```
uses atlas.hcl at the root directory

### check db
```
docker exec -it postgres psql -U postgres -d orchestration

boltbrowser ~/.lo/<siteid>/bolt.db
```
## seeding
docker cp tests/seeds/site.sql postgres:/tmp/site.sql
docker exec -it postgres psql -U postgres -d orchestration -f ./tmp/site.sql

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

