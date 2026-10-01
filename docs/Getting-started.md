
## 🚀 Getting Started

Welcome! This guide helps you **set up and run** the Intelligent Edge Orchestrator in a local development environment so you can explore its CO / LO / EN architecture.

### Prerequisites

- [Podman](https://podman.io/) (or Docker)  
- `podman-compose`  
- Go (version **1.xx** or above)  
- Node.js / npm (for the web portal)  
- A terminal / bash shell  


### 1. Clone the Repository

```bash
git clone https://github.com/balaji-balu/ieo.git
cd ieo
````


### 2. Launch the Core Services

Use `podman-compose` to bring up the three-tier orchestration system:

```bash
podman-compose --env-file .env.staging up -d
```

This will start:

* **CO** (Central Orchestrator)
* **LO** (Local Orchestrator)
* **EN** (Edge Node runtime)

You can verify with:

```bash
podman ps
```

### 3. Start the Web Portal & CLI

* Open the Web Portal:

  ```bash
  cd web-portal
  npm install
  npm run dev
  ```

  Then navigate to: `http://localhost:3000` in your browser.

* Use the CLI:

  ```bash
  cd edgectl
  go run main.go co status
  ```

### 4. Add a Sample App & Deploy

1. **Add the app to the App Store**:

   ```bash
   go run main.go co add app \
     --name "Sample App" \
     --artifact https://github.com/edge-orchestration-platform/app-registry
   ```

2. **List available apps**:

   ```bash
   go run main.go co list apps
   ```

3. **Deploy the app to a site** (replace `SITE_ID` with your actual site ID):

   ```bash
   go run main.go co deploy \
     --app "Sample App" \
     --site SITE_ID \
     --deploytype compose
   ```


### 5. Run Tests

```bash
go test ./...
```

Optionally, run smoke tests via GitHub Actions or local scripts.



### ✅ What Next?

* Dive into the **Architecture** section below to understand how CO / LO / EN communicate.
* Explore `docs/` for more detailed configuration and security guides.
* Check out the **Contributing** guide if you want to build or contribute.

