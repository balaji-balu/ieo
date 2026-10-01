# Installation

Edge-Orch is composed of three independent components:

| Component | Role                 |
| --------- | -------------------- |
| **CO**    | Central Orchestrator |
| **LO**    | Local Orchestrator   |
| **EN**    | Edge Node agent      |

In production, these typically run on **different hosts**.
For demos and development, they may be co-located on a single host.

---

## Design Principles

* ✅ One binary per component
* ✅ One install root per host
* ✅ No hardcoded filesystem paths
* ✅ Same binaries for demo and production
* ✅ Lifecycle managed by **systemd *or* container runtime**

---

## Install Layout (per host)

Each host has a single install root (example: `/opt/ieo`).

```text
/opt/ieo/
├── bin/
│   ├── ieo-co
│   ├── ieo-lo
│   └── ieo-en
├── config/
│   └── ieo/
│       ├── co/co.yaml
│       ├── lo/lo.yaml
│       └── en/en.yaml
├── data/
│   ├── co/
│   ├── lo/
│   └── en/
├── logs/
│   ├── co/
│   ├── lo/
│   └── en/
└── install.yaml
```

💡 A host only contains the components installed on it.

---

## Download

```bash
curl -LO https://github.com/balaji-balu/ieo/releases/download/v0.1.0/ieo_0.1.0_linux_amd64.tar.gz
tar xzf ieo_0.1.0_linux_amd64.tar.gz
cd ieo_0.1.0_linux_amd64
```

---

## Quick Start (Demo – single host)

Install all components on one machine.

```bash
sudo ./edgectl init \
  --root-dir /opt/ieo \
  --units co,lo,en
```

This:

* creates the install layout
* copies binaries
* installs default configs
* generates systemd services
* enables and starts services

### Verify

```bash
edgectl status
```

```text
UNIT  STATE
co    running
lo    running
en   running
```

---

## Production Install (recommended)

### CO host

```bash
sudo ./edgectl init --unit co --root-dir /opt/ieo
```

### LO host

```bash
sudo ./edgectl init --unit lo --root-dir /opt/ieo
```

### EN host(s)

```bash
sudo ./edgectl init --unit en --root-dir /opt/ieo
```

✅ Same binaries
✅ Same layout
✅ Different hosts

---

## Service Management (systemd)

On systemd-based hosts:

```bash
edgectl start en
edgectl stop en
edgectl restart en
edgectl status
```

Under the hood, `edgectl` delegates to `systemctl`.

---

## Containerized Environments

Edge-Orch components may also run as containers.

In this mode:

* lifecycle is managed by Docker / Kubernetes
* systemd and `edgectl start|stop` are **not used**
* filesystem layout is provided via a mounted volume

### Example (Docker)

```bash
docker run -d \
  --name en \
  -e IEO_ROOT=/ieo \
  -v /data/ieo:/ieo \
  ghcr.io/balaji-balu/ieo-en:latest
```

The same layout applies inside the container:

```text
/ieo/config/ieo/en/en.yaml
```

---

## Configuration

Each component reads its own config file:

```text
<ROOT>/config/ieo/<unit>/<unit>.yaml
```

Override if needed:

```bash
en --config /custom/path/en.yaml
```

---

## Environment Variables

| Variable          | Purpose                   |
| ----------------- | ------------------------- |
| `IEO_ROOT`  | Install root (required)   |
| `APP_CONFIG_FILE` | Explicit config file      |
| `APP_CONFIG_DIR`  | Explicit config directory |

---

## Uninstall

```bash
sudo edgectl stop co lo en
sudo rm -rf /opt/ieo
```

---

## Development Mode (no install)

```bash
go run ./cmd/en --config configs/en/dev.yaml
```

---

## Key Rule

> **A component is managed by exactly one supervisor per host:
> systemd *or* a container runtime — never both.**

---

## Architectural Note

> **Co-location is a demo convenience, not a production assumption.**
> All components communicate over the network, even when running on the same host.

