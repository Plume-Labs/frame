# Frame — Turn Any Metal Cluster into a Mainframe

**Frame** transforms a collection of bare-metal servers into a unified, mainframe-inspired computing platform. By combining automated bare-metal provisioning, high-performance RDMA networking, distributed storage, and a GitOps-driven control plane, Frame gives you the power and reliability of enterprise mainframes using commodity hardware.

---

## What is Frame?

Traditional mainframes are monolithic, expensive, and proprietary. Frame takes the opposite approach: it orchestrates a cluster of ordinary servers so they behave as a single, always-on, self-healing supercomputer — a mainframe you actually own and control.

**Core ideas:**
- **Any cluster → mainframe** — plug in bare-metal nodes and Frame provisions, connects, and manages them automatically.
- **No single point of failure** — workloads, storage, and networking are distributed across all nodes with self-healing built in.
- **GitOps-first** — the entire cluster state is declared in Git; changes are applied automatically and auditably.
- **High-performance fabric** — RDMA networking (InfiniBand or RoCE) delivers sub-microsecond latency between nodes, rivalling proprietary interconnects.
- **Live observability** — a mainframe-aesthetic monitoring UI gives operators real-time visibility into every node, workload, and event.

---

## Key Capabilities

| Capability | Details |
|---|---|
| **PXE Provisioning** | Nodes boot over the network and are configured automatically — no manual OS install |
| **Kubernetes Orchestration** | Production-ready, HA control plane for scheduling and lifecycle management |
| **RDMA Networking** | InfiniBand / RoCE device plugin; low-latency MPI and storage traffic |
| **Ceph Distributed Storage** | Self-healing block (RBD) and file (CephFS) storage across all nodes |
| **GitOps CD** | Flux CD or ArgoCD keeps the cluster in sync with the Git source of truth |
| **Hot Node Addition** | Expand the cluster at runtime without downtime or manual intervention |
| **Monitoring UI** | Real-time topology, per-node metrics, event log, and node detail inspection |

---

## Quick Start

### Run the Monitoring UI Locally

```bash
npm install
npm run dev
```

Open http://localhost:5173 to explore the cluster dashboard.

### Deploy a Full Bare-Metal Cluster

```bash
cd deploy
./scripts/bootstrap-cluster.sh
```

The bootstrap script will:
1. Configure the PXE boot server for network-based OS installation
2. Run Ansible playbooks to provision Kubernetes with RDMA support
3. Initialise the Ceph distributed storage cluster
4. Bootstrap GitOps (Flux CD) from this repository
5. Deploy the monitoring UI into the cluster

See [deploy/README.md](deploy/README.md) for detailed step-by-step deployment instructions.

---

## Architecture

```
+------------------------------------------------------------------+
|                          Control Plane                            |
|   +----------+   +------------+   +--------------------------+  |
|   |  etcd    |   | API Server |   | Flux CD / ArgoCD         |  |
|   +----------+   +------------+   +--------------------------+  |
+------------------------------------------------------------------+
                              |
              +---------------+---------------+
              |               |               |
  +-----------+--+  +---------+-----+  +------+-------+
  | Worker Node  |  |  Worker Node  |  | Worker Node  |
  | +----------+ |  | +-----------+ |  | +----------+ |
  | |   Pods   | |  | |   Pods    | |  | |   Pods   | |
  | |  (RDMA)  | |  | |  (RDMA)   | |  | |  (RDMA)  | |
  | +----------+ |  | +-----------+ |  | +----------+ |
  | +----------+ |  | +-----------+ |  | +----------+ |
  | |   Ceph   | |  | |   Ceph    | |  | |   Ceph   | |
  | |   OSD    | |  | |   OSD     | |  | |   OSD    | |
  | +----------+ |  | +-----------+ |  | +----------+ |
  +--------------+  +---------------+  +--------------+
          |                  |                  |
          +------------- RDMA Fabric -----------+
                    (InfiniBand / RoCE)
```

**Data path:** workloads communicate over the RDMA fabric for near-zero-latency inter-node traffic. Storage I/O goes through Ceph OSDs distributed across every node, providing redundancy without a dedicated SAN.

---

## Project Structure

```
.
+-- src/                     # React monitoring UI (TypeScript)
|   +-- components/          # UI components (topology, metrics, event log)
|   +-- lib/                 # Cluster simulation and data logic
|   +-- hooks/               # React hooks for real-time updates
+-- deploy/                  # Infrastructure as Code
|   +-- kubernetes/          # Kustomize manifests for all workloads
|   +-- gitops/              # Flux CD / ArgoCD bootstrap configs
|   +-- ansible/             # Ansible playbooks for bare-metal provisioning
|   +-- pxe/                 # PXE / DHCP / TFTP boot configuration
|   +-- ceph/                # Rook-Ceph operator and cluster CRs
|   +-- networking/          # RDMA device plugin and network tuning
|   +-- monitoring/          # Prometheus + Grafana observability stack
|   +-- scripts/             # Utility scripts (bootstrap, health-check, hot-add)
+-- Dockerfile               # Container image for the monitoring UI
+-- PRD.md                   # Product requirements and design direction
```

---

## Technology Stack

### Monitoring UI

| Component | Version / Notes |
|---|---|
| React + TypeScript | React 19, strict mode |
| Tailwind CSS + shadcn/ui | Utility-first styling with accessible components |
| Framer Motion | Smooth animations for live data updates |
| Vite | Fast build and HMR for development |

### Infrastructure

| Component | Version / Notes |
|---|---|
| Kubernetes | 1.28+, HA control plane (3+ etcd replicas) |
| Containerd | CRI runtime |
| Calico CNI + Multus | Pod networking + secondary RDMA interfaces |
| Rook Ceph | 1.13+, RBD block storage and CephFS |
| Flux CD / ArgoCD | GitOps continuous delivery |
| Ansible | 2.15+, idempotent bare-metal provisioning |
| Prometheus + Grafana | Cluster and workload observability |

### Networking

| Component | Details |
|---|---|
| InfiniBand / RoCE | RDMA transport for < 1 μs inter-node latency |
| Mellanox OFED | Drivers and user-space verbs library |
| RDMA Device Plugin | Kubernetes device plugin exposing RDMA resources to pods |
| Network tuning | IRQ affinity, MTU, ECN / DCQCN for RoCE |

---

## Deployment Guides

- [Full Deployment Overview](deploy/README.md)
- [Kubernetes Manifests](deploy/kubernetes/README.md)
- [GitOps Setup (Flux / ArgoCD)](deploy/gitops/README.md)
- [Ansible Bare-Metal Playbooks](deploy/ansible/README.md)
- [PXE Boot Configuration](deploy/pxe/README.md)
- [Ceph Distributed Storage](deploy/ceph/README.md)
- [RDMA Networking](deploy/networking/README.md)

### Utility Scripts (`deploy/scripts/`)

| Script | Purpose |
|---|---|
| `bootstrap-cluster.sh` | End-to-end cluster bootstrap from bare metal |
| `hot-add-node.sh` | Add a new node to a running cluster without downtime |
| `health-check.sh` | Verify cluster health (nodes, storage, networking, GitOps) |

---

## Prerequisites

**Bare-metal nodes:**
- RDMA-capable NIC (InfiniBand HCA or RoCE NIC)
- PXE boot support (UEFI or legacy BIOS)
- IPMI / BMC for remote power management

**Operator workstation:**
- Ansible 2.15+
- kubectl 1.28+
- Flux CLI 2.2+ or ArgoCD CLI 2.9+
- Python 3.11+

---

## License

Licensed under the MIT License. See [LICENSE](LICENSE) for details.
