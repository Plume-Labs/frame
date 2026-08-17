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
│  frame.plume-labs.io/v1beta1 CRDs (8)                   │
│  ┌──────────────┐ ┌───────────────┐ ┌─────────────────┐ │
│  │  FrameJob    │ │  FrameNode    │ │SchedulingPolicy │ │
│  └──────────────┘ └───────────────┘ └─────────────────┘ │
│  ┌──────────────┐ ┌───────────────┐ ┌─────────────────┐ │
│  │FrameResource │ │TalosMachine   │ │  TalosUpgrade   │ │
│  │    Quota     │ │    Config     │ │                 │ │
│  └──────────────┘ └───────────────┘ └─────────────────┘ │
│  ┌──────────────┐ ┌───────────────┐                     │
│  │  FrameUser   │ │  NodeTuning   │ ← cluster-scoped    │
│  └──────────────┘ └───────────────┘   v1beta1 only      │
│                                                          │
│  services.plume-labs.io/v1beta1 CRDs (1)                │
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
│  NodeTuning           → node agent + gated restart       │
│                                                          │
│  Webhooks: validation on 8 kinds, defaulting on 2        │
│  (NodeTuning has none)                                   │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────┐
│        Node agent DaemonSet (cmd/agent) — every node     │
│  observes measured state · applies node-local settings   │
│  restarts only k3s|k3s-agent|kubelet|containerd          │
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

- The React 19 UI (`src/components/`) and the TypeScript SDK (`src/lib/frame-sdk.ts`) both call the Kubernetes API directly at `/apis/frame.plume-labs.io/v1beta1/…`.
- **Dev:** `kubectl proxy --port=8001` exposes the K8s API locally; Vite proxies `/apis/*` to it (see `vite.config.ts`).
- **Prod:** `window.__FRAME_TOKEN__` is set to a ServiceAccount Bearer token before the app mounts. The SDK picks it up via `Authorization: Bearer <token>`.
- There is **no intermediate API server**. The UI is fully K8s-native.

**Key files:**

| Path | Role |
|---|---|
| `src/lib/frame-sdk.ts` | `FrameClient` — CRUD over six of the nine kinds. FrameUser is authd's and has no SDK surface; FrameService and NodeTuning have none yet. |
| `src/components/` | React control surfaces (Jobs, Scheduler, Nodes, …) |
| `src/hooks/` | Real-time update hooks |
| `vite.config.ts` | Dev proxy: `/apis` → `localhost:8001` |

---

## Layer 2 — Operator (`api/`, `internal/`, `cmd/`)

**What it is:** a Kubebuilder v4 operator, multi-group: `frame.plume-labs.io/v1beta1` (eight CRDs) and `services.plume-labs.io/v1beta1` (`FrameService`). Eight of the nine also serve a deprecated `v1alpha1` through a conversion webhook; `NodeTuning` was added after the freeze and has no `v1alpha1` to convert from. It reconciles eight of the nine CRDs into real cluster effects. FrameUser has no controller — nothing reconciles a user account; every other CRD, including `FrameService` and `NodeTuning`, does.

**Entry points:** `cmd/main.go` — the controller-runtime manager: registers all controllers and webhooks, wires Prometheus metrics. Two more Go binaries ship alongside it: `cmd/authd` (per-user auth, reads `FrameUser`) and `cmd/agent` (the node-tuning agent, a DaemonSet). Four Dockerfiles: `Dockerfile.controller`, `Dockerfile.authd`, `Dockerfile.agent`, and the unsuffixed `Dockerfile` for the React UI.

**Controllers:**

| Controller | Real effect |
|---|---|
| `FrameJob` | Creates/updates/deletes an Argo `Workflow`; syncs `spec.suspend` → `Workflow.spec.suspend`; secondary-watches Argo Workflows for event-driven `Ready`-condition updates |
| `FrameNode` | Secondary-watches core `v1.Node` (label mapping `nodeToFrameNode`) to reflect readiness and versions into status |
| `SchedulingPolicy` | Reconciles a `PriorityClass`; when Volcano/YuniKorn CRD is present, also reconciles the scheduler-native queue. Gracefully degrades when CRD is absent |
| `TalosMachineConfig` | Builds a Talos gRPC client from a referenced Secret; calls `ApplyConfiguration` with inline patch or ConfigMap ref |
| `TalosUpgrade` | Calls Talos gRPC `Upgrade`; generation-based idempotency guard prevents re-trigger on unchanged spec |
| `FrameResourceQuota` | Projects a `corev1.ResourceQuota` into every namespace labelled with the matching service class, and aggregates their `status.used` back into `status.used`/`status.namespaces`. Scheduler queue limits are deliberately not projected — `SchedulingPolicy` owns those |
| `FrameService` | Dispatches to a registered provider (`internal/services/provider/`) by `spec.type`; the `inference` provider creates a llama.cpp Deployment + Service, sized from `spec.parameters`, and a credentials Secret |
| `NodeTuning` | Diffs desired against the state the node agent **measured**, and reports per node. Disruptive changes wait for a per-node, per-generation approval annotation, then roll one node at a time: cordon → evict → detached unit restart → verify `ActiveEnterTimestamp` moved → uncordon. Any failure halts the campaign with the node left cordoned |

All controllers follow the same pattern: add finalizer on create, reconcile desired → actual, sync `.status` + conditions, emit a Kubernetes Event, clean up on delete. `NodeTuning` is the exception on status: it reports a `phase` per node in `status.nodes[]` rather than writing a `Ready` condition — see [crd-reference.md](crd-reference.md).

**Node agent** (`cmd/agent`, DaemonSet in `deploy/kubernetes/base/node-tuning-agent/`): the other half of NodeTuning. Runs on every node — tainted ones and the control-plane server included — privileged with `hostPID`, so every host command is `nsenter`ed into PID 1's namespaces. It observes every 30 s and publishes what the node *measures* (`systemctl show -p MemoryKSM`, the KSM sysfs counters, tuned's active profile), never what it wrote. Because privileged + `hostPID` is host-root-equivalent, the units it may restart are a compile-time allowlist — `k3s`, `k3s-agent`, `kubelet`, `containerd`, exact match — and that allowlist is its entire security boundary. It supersedes the old `ksm-tuner` DaemonSet, which could only report what it had written.

**Webhooks** (`internal/webhook/frame/v1beta1/` and `internal/webhook/services/v1beta1/`): validation on eight of the nine kinds — `NodeTuning` has none, its bounds being carried by CRD schema markers alone; defaulting on FrameNode and FrameJob only. They register on `v1beta1` alone — `matchPolicy: Equivalent` converts a `v1alpha1` request to the storage version before dispatch, so one registration covers both. Cert-manager manages TLS; see `config/certmanager/`.

**Conversion webhook** (`api/frame/v1alpha1/conversion.go`, `api/services/v1alpha1/conversion.go`): every CRD that has two versions — the eight pre-freeze kinds, so all but `NodeTuning` — declares `conversion.strategy: Webhook` pointing at the manager's `/convert`, with the same cert-manager-injected CA. `v1beta1` is the hub; `v1alpha1` is the spoke and implements `ConvertTo`/`ConvertFrom`. Seven kinds are field-wise strict subsets of `v1alpha1`, and `FrameUser` is a bijection (`spec.passwordHash` ↔ `status.passwordHash`), so no annotation escape hatch is needed anywhere. controller-runtime registers `/convert` implicitly, through the same `registerWebhooks()` every `SetupXWebhookWithManager` already goes through — which means **both** GVKs must be in `cmd/main.go`'s scheme or `conversion.IsConvertible` sees one version per GroupKind, `/convert` is never registered, and every `v1alpha1` request 404s against CRDs that declare `strategy: Webhook`. `cmd/scheme_test.go` pins that.

Two consequences worth knowing before changing anything here. Conversion output is stored **without re-validation**, so a `v1alpha1` write is the only way to violate a bound `v1beta1` adds. And it is not re-defaulted either, so a CEL rule dereferencing an `omitempty` scalar must be `has()`-guarded on both versions.

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
| `deploy/kubernetes/` | Kustomize base + overlays (development, production) for the UI, plus the node-tuning agent DaemonSet and its RBAC |
| `deploy/caching/` | Alluxio, NVMe burst buffer, Redis cluster, vLLM RDMA KV-cache |
| `deploy/resilience/` | Velero backups, checkpoint controller |
| `deploy/certmanager/` | ClusterIssuer |
| `deploy/terraform/` | Terraform entry point for the bare-metal substrate |
| `deploy/samples/` | Sample workloads for the test cluster |
| `deploy/docker/` | nginx config baked into the UI image |
| `deploy/scripts/` | Bootstrap, health-check, hot-add, and per-component `*-up.sh` installers — including `node-tuning-install.sh` |

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
FrameClient.jobs.submit() — POST /apis/frame.plume-labs.io/v1beta1/namespaces/<ns>/framejobs
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
  → updates the FrameJob's Ready condition (status + reason) + emits K8s Event
        │
        ▼
UI's watch stream fires → re-reads the FrameJob → reflects live status
```
