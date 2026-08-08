# Architecture

Frame is three cooperating layers in one repo.

---

## Overview

```
┌──────────────────────────────────────────────────────────┐
│                     Frame Control Plane                  │
│                                                          │
│   React UI (src/)          TypeScript SDK (frame-sdk.ts) │
│        │                          │                      │
│        └──────────┬───────────────┘                      │
│                   │  fetch /apis/frame.plume-labs.io/…   │
│                   ▼                                      │
│         kubectl proxy (dev)  /  ServiceAccount (prod)    │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────┐
│                  Kubernetes API Server                   │
│                                                          │
│  frame.plume-labs.io/v1alpha1 CRDs (7)                  │
│  ┌──────────────┐ ┌───────────────┐ ┌─────────────────┐ │
│  │  FrameJob    │ │  FrameNode    │ │SchedulingPolicy │ │
│  └──────────────┘ └───────────────┘ └─────────────────┘ │
│  ┌──────────────┐ ┌───────────────┐ ┌─────────────────┐ │
│  │FrameResource │ │TalosMachine   │ │  TalosUpgrade   │ │
│  │    Quota     │ │    Config     │ │     FrameUser   │ │
│  └──────────────┘ └───────────────┘ └─────────────────┘ │
│                                                          │
│  services.plume-labs.io/v1alpha1 CRDs (1)               │
│  ┌──────────────┐                                       │
│  │ FrameService │                                       │
│  └──────────────┘                                       │
└──────────────────────┬───────────────────────────────────┘
                       │  controller-runtime watches
┌──────────────────────▼───────────────────────────────────┐
│              Frame Operator (internal/)                  │
│                                                          │
│  FrameJob controller  → Argo Workflow                    │
│  FrameNode controller → core v1.Node watch               │
│  SchedulingPolicy     → PriorityClass + Volcano/YuniKorn │
│  TalosMachineConfig   → Talos gRPC ApplyConfiguration    │
│  TalosUpgrade         → Talos gRPC Upgrade               │
│  FrameResourceQuota   → namespace ResourceQuota          │
│  FrameService         → per-type provider (llama.cpp …) │
│                                                          │
│  Webhooks: validation on 8 kinds, defaulting on 2        │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────┐
│           Cluster Primitives (deploy/)                   │
│                                                          │
│  Argo Workflows · PriorityClasses · ResourceQuotas       │
│  Talos MachineConfigs · Omni (planned) · PXE boot        │
│  Ceph (Rook, RGW) · Cilium · SR-IOV · RDMA               │
│  Prometheus · Grafana · Jaeger · DCGM · OpenLineage      │
└──────────────────────────────────────────────────────────┘
```

---

## Layer 1 — Control plane (`src/`)

**What it is:** the operator-facing surface. Humans and CI pipelines use it to submit jobs, manage scheduling policies, set quotas, and inspect cluster state.

**How it works:**

- The React 19 UI (`src/components/`) and the TypeScript SDK (`src/lib/frame-sdk.ts`) both call the Kubernetes API directly at `/apis/frame.plume-labs.io/v1alpha1/…`.
- **Dev:** `kubectl proxy --port=8001` exposes the K8s API locally; Vite proxies `/apis/*` to it (see `vite.config.ts`).
- **Prod:** `window.__FRAME_TOKEN__` is set to a ServiceAccount Bearer token before the app mounts. The SDK picks it up via `Authorization: Bearer <token>`.
- There is **no intermediate API server**. The UI is fully K8s-native.

**Key files:**

| Path | Role |
|---|---|
| `src/lib/frame-sdk.ts` | `FrameClient` — CRUD over six of the eight kinds. FrameUser is authd's and has no SDK surface; FrameService has none yet. |
| `src/components/` | React control surfaces (Jobs, Scheduler, Nodes, …) |
| `src/hooks/` | Real-time update hooks |
| `vite.config.ts` | Dev proxy: `/apis` → `localhost:8001` |

---

## Layer 2 — Operator (`api/`, `internal/`, `cmd/`)

**What it is:** a Kubebuilder v4 operator, multi-group: `frame.plume-labs.io/v1alpha1` (seven CRDs) and `services.plume-labs.io/v1alpha1` (`FrameService`). It reconciles seven of the eight CRDs into real cluster effects. FrameUser has no controller — nothing reconciles a user account; every other CRD, including `FrameService`, does.

**Entry point:** `cmd/main.go` — starts the controller-runtime manager, registers all controllers and webhooks, wires Prometheus metrics.

**Controllers:**

| Controller | Real effect |
|---|---|
| `FrameJob` | Creates/updates/deletes an Argo `Workflow`; syncs `spec.suspend` → `Workflow.spec.suspend`; secondary-watches Argo Workflows for event-driven phase updates |
| `FrameNode` | Secondary-watches core `v1.Node` (label mapping `nodeToFrameNode`) to reflect readiness and versions into status |
| `SchedulingPolicy` | Reconciles a `PriorityClass`; when Volcano/YuniKorn CRD is present, also reconciles the scheduler-native queue. Gracefully degrades when CRD is absent |
| `TalosMachineConfig` | Builds a Talos gRPC client from a referenced Secret; calls `ApplyConfiguration` with inline patch or ConfigMap ref |
| `TalosUpgrade` | Calls Talos gRPC `Upgrade`; generation-based idempotency guard prevents re-trigger on unchanged spec |
| `FrameResourceQuota` | Validates namespace quotas (webhooks); projection into `ResourceQuota` + scheduler limits in progress |
| `FrameService` | Dispatches to a registered provider (`internal/services/provider/`) by `spec.type`; the `inference` provider creates a llama.cpp Deployment + Service, sized from `spec.parameters`, and a credentials Secret |

All controllers follow the same pattern: add finalizer on create, reconcile desired → actual, sync `.status` + conditions, emit a Kubernetes Event, clean up on delete.

**Webhooks** (`internal/webhook/frame/v1alpha1/` and `internal/webhook/services/v1alpha1/`): validation on all eight kinds; defaulting on FrameNode and FrameJob only. Cert-manager manages TLS; see `config/certmanager/`.

---

## Layer 3 — Infrastructure as code (`deploy/`)

Everything needed to stand up the bare-metal cluster the operator runs on.

| Directory | What it provisions |
|---|---|
| `deploy/talos/` | Talos MachineConfigs, Image Factory schematics |
| `deploy/omni/` | Omni bare-metal server lifecycle (prepared, not deployed) |
| `deploy/pxe/` | PXE / DHCP / TFTP boot configuration |
| `deploy/ceph/` | Rook-Ceph operator + cluster CRs (block + file storage) |
| `deploy/networking/` | Cilium, SR-IOV device plugin, DPDK, RDMA device plugin |
| `deploy/monitoring/` | Prometheus + Grafana + Jaeger + DCGM Exporter |
| `deploy/jobs/` | Argo Workflows templates and DAG manifests |
| `deploy/storage/` | Ceph RGW (S3), DataHub, data fabric namespace |
| `deploy/gitops/` | Flux CD / ArgoCD bootstrap configs |
| `deploy/kubernetes/` | Kustomize base + overlays (development, production) for the UI |
| `deploy/scripts/` | Bootstrap, health-check, hot-add scripts |

---

## Cluster topology constraints

Frame is designed for a **single physical location**:

- One or more racks in the same building (same L2 network segment for RDMA)
- RDMA fabric (InfiniBand or RoCE) is a **local interconnect** — it does not traverse WAN or internet links
- `zones` and `racks` in Frame are **failure-domain labels within the same site**, not geographic regions
- Multi-site / multi-region federation is explicitly **out of scope** for this version

---

## Data flow — job submission

```
User clicks "Submit" in UI
        │
        ▼
FrameClient.jobs.submit() — POST /apis/frame.plume-labs.io/v1alpha1/namespaces/<ns>/framejobs
        │
        ▼
K8s API server validates (webhook: FrameJob defaulting + validation)
        │
        ▼
FrameJob CR stored in etcd
        │
        ▼
FrameJob controller reconciles — creates Argo Workflow CR with:
  - spec.suspend from job.spec.suspended
  - priorityClassName from job.spec.priority
  - gpu-count parameter from job.spec.gpuCount
        │
        ▼
Argo Workflow controller runs the DAG on the cluster
        │
        ▼
FrameJob controller secondary-watch detects Workflow phase change
  → updates FrameJob.status.phase + emits K8s Event
        │
        ▼
UI polls FrameJob CR → reflects live status
```
