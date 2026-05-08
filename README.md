# Frame — Mainframe Framework for Kubernetes

**Frame** is an open-source framework that turns a collection of bare-metal servers into a unified, mainframe-grade computing platform. It provides the full control-plane stack — scheduling, job orchestration, resource management, resilience, and observability — through both a rich operator UI and a REST API that workloads and CI pipelines can call directly.

---

## What is Frame?

Traditional mainframes are monolithic, expensive, and proprietary. Frame takes the opposite approach: it orchestrates a cluster of ordinary servers so they behave as a single, always-on, self-healing supercomputer — a mainframe you actually own and control.

**Core ideas:**
- **Framework, not just a dashboard** — Frame exposes a REST API and TypeScript SDK so operators, workloads, and CI pipelines can interact with the platform programmatically: submit jobs, tune policies, adjust quotas, and inspect state without touching the UI.
- **Any local cluster → mainframe** — plug in bare-metal nodes in a single location (one or more racks) and Frame provisions, connects, and manages them automatically via Talos + Sidero Metal + GitOps.
- **No single point of failure** — workloads, storage, and networking are distributed across all nodes with self-healing built in (Velero snapshots, checkpoint controller, IPMI watchdog).
- **GitOps-first** — the entire cluster state is declared in Git; changes are applied automatically and auditably via Flux CD / ArgoCD.
- **High-performance local fabric** — RDMA networking (InfiniBand or RoCE) with Cilium eBPF, SR-IOV, and DPDK delivers sub-microsecond latency between nodes within the same physical location. RDMA is a **local, intra-datacenter interconnect** — it is not stretched over the internet or across sites.
- **Framework-first UI** — the control plane UI leads with job submission, scheduling policy management, resource provisioning, and resilience controls; observability dashboards are a secondary concern.

> **Scope:** Frame manages a **single local cluster** — one physical location, one or more racks. Multi-site / multi-region federation is intentionally **out of scope** for this version and will require a separate mechanism (WAN overlay, API aggregation, etc.) when it is added later.

---

## Key Capabilities

| Capability | Details |
|---|---|
| **Operator REST API** | HTTP API (+ TypeScript SDK) for job submission, policy management, quota control, and node inspection |
| **Job Orchestration** | Argo Workflows DAGs with TLS+SSO; inference and training pipelines with checkpoint/resume |
| **Advanced Scheduling** | Volcano / YuniKorn with PriorityClasses, PodGroups, gang-scheduling, and preemption |
| **Service Classes** | HIGH / MEDIUM / LOW QoS tiers enforced at the scheduler, node, and network layers |
| **GPU & MIG Management** | Per-GPU utilisation, MIG partitioning, DCGM metrics, SM occupancy |
| **Data Locality & Memory** | Alluxio cache, Redis, NVMe tiering; data-aware scheduling to minimise movement |
| **Unified Storage** | MinIO object storage, Ceph block/file (via Rook), DataHub metadata |
| **High-Performance Networking** | Cilium + eBPF, SR-IOV device plugin, DPDK, InfiniBand RDMA |
| **Resilience & Reliability** | Velero backup/restore, checkpoint controller, IPMI watchdog, zone-aware replication |
| **Observability** | Prometheus + Grafana, Jaeger distributed tracing, DCGM exporter, OpenLineage data lineage |
| **PXE Provisioning** | Nodes boot over the network and are configured automatically — no manual OS install |
| **GitOps CD** | Flux CD / ArgoCD keeps the cluster in sync with the Git source of truth |

---

## Quick Start

### Run the Control Plane UI and API Server Locally

```bash
npm install
npm run dev       # React UI  →  http://localhost:5173
npm run server    # REST API  →  http://localhost:4000
```

### Use the TypeScript SDK

```typescript
import { FrameClient } from '@/lib/frame-sdk'

const frame = new FrameClient('http://localhost:4000')

// Submit a GPU training job
const job = await frame.jobs.submit({
  name:         'llm-finetune-v4',
  pipeline:     'training',
  serviceClass: 'HIGH',
  priority:     'critical',
  namespace:    'neura-prod',
  gpuCount:     8,
})

// Apply a scheduling policy
await frame.scheduler.applyPolicy({
  name:       'hpc-critical',
  scheduler:  'volcano',
  queue:      'hpc',
  priority:   100,
  preemption: true,
  maxGPUs:    64,
  maxCPUs:    512,
})

// Inspect cluster nodes
const { items: nodes } = await frame.nodes.list()
```

### Deploy a Full Bare-Metal Cluster

```bash
cd deploy
./scripts/bootstrap-talos.sh <controlplane-ip> <worker-ips-comma-separated>
./scripts/neura-bootstrap.sh      # HPC / Neura stack
```

See [deploy/README.md](deploy/README.md) for deployment details and [deploy/talos/README.md](deploy/talos/README.md) for Talos bootstrap specifics.

### Provisioning (Talos-native)

The provisioning flow is now:

1. Build Talos artifacts with Image Factory schematics in `deploy/talos/schematics/`
2. Generate and patch MachineConfigs from `deploy/talos/`
3. Provision and classify bare-metal servers with Sidero Metal (`deploy/sidero/`)
4. Reconcile platform services and tuning components through Flux GitOps

`deploy/ansible/` is now deprecated and kept temporarily for migration only. It will be removed in a future release.
Use the assets under `deploy/talos/` and `deploy/sidero/` for all new provisioning workflows.

---

## Operator API

Frame exposes a REST API so that any tooling can interact with the framework without the UI.

**API spec:** [`deploy/api/openapi.yaml`](deploy/api/openapi.yaml) (OpenAPI 3.1)

| Endpoint | Method | Description |
|---|---|---|
| `/api/health` | GET | API health and version |
| `/api/nodes` | GET | List cluster nodes |
| `/api/nodes/:id` | GET | Get a node by ID |
| `/api/jobs` | GET | List all jobs |
| `/api/jobs` | POST | Submit a new job |
| `/api/jobs/:id` | DELETE | Cancel a job |
| `/api/scheduler/policies` | GET | List scheduling policies |
| `/api/scheduler/policies` | POST | Create / update a policy |
| `/api/scheduler/policies/:name` | DELETE | Delete a policy |
| `/api/resources/quotas` | GET | List resource quotas |
| `/api/resources/quotas/:ns` | PUT | Create / update a namespace quota |
| `/api/resources/service-classes` | GET | Service-class node summary |

---

## Architecture

```
+------------------------------------------------------------------+
|                       Frame Control Plane                        |
|  +------------------+  +------------------+  +--------------+   |
|  |  REST API Server |  | TypeScript SDK   |  |  React UI    |   |
|  |  (Express / :4000)|  | (frame-sdk.ts)   |  |  (:5173)     |   |
|  +--------+---------+  +------------------+  +------+-------+   |
|           |                                         |            |
|           +-------------------+---------------------+            |
|                               |                                  |
|   +---------------------------+-------------------------------+  |
|   |            Kubernetes Control Plane                       |  |
|   |  +----------+  +------------+  +------------------------+ | |
|   |  |  etcd    |  | API Server |  | Flux CD / ArgoCD       | | |
|   |  +----------+  +------------+  +------------------------+ | |
|   |                                                           |  |
|   |  Volcano / YuniKorn scheduler     Argo Workflows          |  |
|   +-----------------------------------------------------------+  |
+------------------------------------------------------------------+
                              |
              +---------------+---------------+
              |               |               |
  +-----------+--+  +---------+-----+  +------+-------+
  | Worker Node  |  |  Worker Node  |  | Worker Node  |
  | GPU / RDMA   |  |  GPU / RDMA   |  |  GPU / RDMA  |
  | Ceph OSD     |  |  Ceph OSD     |  |  Ceph OSD    |
  +--------------+  +---------------+  +--------------+
          |                  |                  |
          +------------- RDMA Fabric (local — same datacenter) -----------+
                    (InfiniBand / RoCE + Cilium eBPF, intra-rack/inter-rack)
```

---

## UI — Framework-First Navigation

The control-plane UI leads with operator actions and puts observability dashboards secondary:

| Tab | Purpose |
|---|---|
| **Jobs** | Submit, inspect, and cancel Argo Workflow jobs |
| **Scheduler** | Manage PriorityClasses, PodGroups, and queue policies (Volcano / YuniKorn) |
| **Service Classes** | Assign and monitor HIGH / MEDIUM / LOW QoS tiers across nodes |
| **Data Locality** | Alluxio cache stats, storage-tier heatmap, data-aware placement |
| **Storage** | MinIO, Ceph, and DataHub fabric dashboard |
| **GPU** | Per-GPU utilisation, MIG instances, SM occupancy, NVLink BW |
| **Resilience** | Velero snapshots, checkpoint status, IPMI watchdog health |
| **Nodes** | Node topology grid with status and metrics |
| **Racks** | Rack visualisation and drag-and-drop management |
| **Zones** | Failure-domain heatmap and zone detail view |
| **Observe** | Aggregate metrics, capacity forecasting, and anomaly alerts |
| **Lineage** | OpenLineage data provenance graph |
| **Events** | Cluster event log |

---

## Project Structure

```
.
+-- src/
|   +-- components/       # React UI components (control surfaces + dashboards)
|   +-- lib/              # Cluster simulation, types, analytics, Frame SDK
|   |   +-- frame-sdk.ts  # TypeScript operator SDK (FrameClient)
|   +-- hooks/            # React hooks for real-time updates
+-- server/
|   +-- index.ts          # Frame REST API server (Express)
+-- deploy/
|   +-- api/
|   |   +-- openapi.yaml  # OpenAPI 3.1 spec for the Frame operator API
|   +-- kubernetes/       # Kustomize manifests for all workloads
|   +-- gitops/           # Flux CD / ArgoCD bootstrap configs
|   +-- ansible/          # Ansible playbooks for bare-metal provisioning
|   +-- pxe/              # PXE / DHCP / TFTP boot configuration
|   +-- ceph/             # Rook-Ceph operator and cluster CRs
|   +-- networking/       # Cilium, SR-IOV, DPDK, RDMA device plugin
|   +-- monitoring/       # Prometheus + Grafana + Jaeger + DCGM
|   +-- jobs/             # Argo Workflows manifests and DAG templates
|   +-- storage/          # MinIO, DataHub, Ceph data-fabric
|   +-- scripts/          # Utility scripts (bootstrap, health-check, hot-add)
+-- Dockerfile            # Container image for the Frame UI + API server
+-- PRD.md                # Product requirements and design direction
```

---

## Technology Stack

### Control Plane

| Component | Version / Notes |
|---|---|
| React + TypeScript | React 19, strict mode |
| Tailwind CSS + shadcn/ui | Utility-first styling with accessible components |
| Express 5 | REST API server |
| Framer Motion | Smooth animations for live data updates |
| Vite | Fast build and HMR for development |

### Scheduler & Orchestration

| Component | Version / Notes |
|---|---|
| Volcano | Gang-scheduling, PodGroups, fair-share queues |
| YuniKorn | Hierarchical queues, preemption |
| Argo Workflows | DAG pipelines with TLS + SSO; checkpoint/resume support |

### Infrastructure

| Component | Version / Notes |
|---|---|
| Kubernetes | 1.28+, HA control plane (3+ etcd replicas) |
| Containerd | CRI runtime |
| Cilium + Multus | eBPF networking, SR-IOV secondary interfaces |
| Talos Linux | v1.9+ — immutable OS, API-driven |
| Sidero Metal | v0.6+ — bare-metal provisioner Kubernetes-native |
| Node Feature Discovery | v0.16+ — auto-labelling hardware |
| Rook Ceph | 1.13+, RBD block storage and CephFS |
| MinIO | S3-compatible object storage |
| Flux CD / ArgoCD | GitOps continuous delivery |
| Prometheus + Grafana | Cluster and workload observability |
| Jaeger | Distributed tracing |
| DCGM Exporter | GPU metrics |
| OpenLineage | Data lineage tracking |

### Networking

| Component | Details |
|---|---|
| InfiniBand / RoCE | RDMA transport for < 1 μs intra-datacenter latency (local fabric only — not for WAN/internet) |
| Cilium eBPF | High-performance pod networking and network policy |
| SR-IOV | Hardware-level NIC virtualisation for GPU workloads |
| DPDK | Kernel-bypass packet processing |

---

## Deployment Guides

- [Full Deployment Overview](deploy/README.md)
- [Kubernetes Manifests](deploy/kubernetes/README.md)
- [GitOps Setup (Flux / ArgoCD)](deploy/gitops/README.md)
- [Talos MachineConfigs and Schematics](deploy/talos/README.md)
- [Sidero Metal Resources](deploy/sidero/README.md)
- [Ansible Bare-Metal Playbooks](deploy/ansible/README.md)
- [PXE Boot Configuration](deploy/pxe/README.md)
- [Ceph Distributed Storage](deploy/ceph/README.md)
- [RDMA Networking](deploy/networking/README.md)
- [Operator API Reference](deploy/api/openapi.yaml)

### Utility Scripts (`deploy/scripts/`)

| Script | Purpose |
|---|---|
| `bootstrap-cluster.sh` | End-to-end cluster bootstrap from bare metal |
| `bootstrap-talos.sh` | Talos-native cluster bootstrap (MachineConfig + Flux) |
| `neura-bootstrap.sh` | HPC / Neura stack (GPU, Argo, MinIO, Jaeger, …) |
| `hot-add-node.sh` | Add a new node to a running cluster without downtime |
| `health-check.sh` | Verify cluster health (nodes, storage, networking, GitOps) |

---

## Cluster Topology Constraints

Frame is designed for a **single physical location**:

- One or more racks in the same room / building (same Layer-2 network segment for RDMA)
- RDMA fabric (InfiniBand or RoCE) is a **local interconnect** — it does not traverse the internet or WAN links
- `zones` and `racks` in Frame are **failure-domain labels within the same site**, not geographic regions
- **Multi-site / multi-region federation** is explicitly out of scope for the current version; connecting two Frame clusters across locations will require a separate mechanism (WAN gateway, API aggregation layer, or a dedicated federation controller) to be defined in a future release

---

## Prerequisites

**Bare-metal nodes:**
- RDMA-capable NIC (InfiniBand HCA or RoCE NIC)
- PXE boot support (UEFI or legacy BIOS)
- IPMI / BMC for remote power management

**Operator workstation:**
- talosctl 1.9+
- kubectl 1.28+
- Flux CLI 2.2+ or ArgoCD CLI 2.9+
- Node.js 20+ (to run the Frame API server and UI)

---

## License

Licensed under the MIT License. See [LICENSE](LICENSE) for details.
