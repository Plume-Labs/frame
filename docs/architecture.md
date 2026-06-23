# Architecture

Frame is three cooperating layers in one repo.

## 1. Control plane — `src/`, `server/`

A React 19 + Tailwind/shadcn UI plus an Express 5 REST API and a TypeScript SDK
(`FrameClient`). This is the operator-facing surface: submit jobs, edit
scheduling policies, set quotas, inspect nodes.

- UI: `src/components/` (control surfaces + dashboards), `src/hooks/`
  (real-time updates), `src/lib/frame-sdk.ts` (SDK).
- API: `server/index.ts` (Express), `server/routes/`.
- API spec: [`deploy/api/openapi.yaml`](../deploy/api/openapi.yaml) (OpenAPI 3.1).

> **Current state:** the API server backs requests with an in-memory cluster
> simulation, and the UI falls back to simulated data when the API is offline.
> It does **not** yet read/write the operator CRDs. Closing that gap is the
> headline V1 work item — see [roadmap.md](roadmap.md).

## 2. Operator — `api/`, `internal/`, `cmd/`

A Kubebuilder (`go.kubebuilder.io/v4`) operator, group `frame.plume-labs.io`,
version `v1alpha1`, that reconciles six CRDs. This layer is what actually
mutates cluster state.

```
cmd/main.go                       Manager entry — registers controllers + webhooks
api/v1alpha1/*_types.go           CRD schemas (+kubebuilder markers)
internal/controller/*             Reconcilers
internal/webhook/v1alpha1/*       Validating / defaulting webhooks
config/                           Generated CRDs, RBAC, webhook + kustomize bases
```

Each CRD has a controller and a webhook; full field-level detail lives in
[crd-reference.md](crd-reference.md). Reconcilers follow the standard pattern:
add a finalizer, reconcile desired state, sync `.status` + conditions, emit a
Kubernetes Event (`kubectl describe`), clean up on delete.

Notable behaviours:

- **FrameJob** → renders an Argo `Workflow` with `priorityClassName`, `gpu-count`
  param, and `spec.suspend`. Secondary-watches Argo `Workflow` objects (label
  mapping) so phase updates are event-driven, not just 30 s polling.
  Suspend/resume lifecycle fully supported. Removes the workflow on delete via finalizer.
- **FrameNode** → secondary-watches core `v1.Node` objects (`nodeToFrameNode`
  mapping) to reflect readiness/versions into status.
- **SchedulingPolicy** → reconciles a `PriorityClass` and, when a Volcano or
  YuniKorn CRD is present, the scheduler-native queue. Gracefully degrades when
  the scheduler CRD is missing.
- **TalosMachineConfig / TalosUpgrade** → drive real Talos gRPC calls
  (`ApplyConfiguration` / `Upgrade`) using a TLS client built from a referenced
  Secret. TalosUpgrade uses a generation-based guard to prevent duplicate triggers.

## 3. Infrastructure as code — `deploy/`

Everything needed to stand up the bare-metal cluster the operator runs on:

| Path | Role |
|---|---|
| `deploy/talos/` | Talos MachineConfigs + Image Factory schematics |
| `deploy/sidero/` | Sidero Metal server lifecycle / classification |
| `deploy/pxe/` | PXE / DHCP / TFTP network boot |
| `deploy/ceph/` | Rook-Ceph block + file storage |
| `deploy/storage/` | MinIO, DataHub data fabric |
| `deploy/networking/` | Cilium eBPF, SR-IOV, DPDK, RDMA device plugin |
| `deploy/caching/` | Alluxio, Redis, NVMe burst buffer, vLLM KV cache |
| `deploy/jobs/` | Argo Workflows manifests + DAG templates |
| `deploy/monitoring/` | Prometheus, Grafana, Jaeger, DCGM |
| `deploy/gitops/` | Flux CD / ArgoCD bootstrap |
| `deploy/terraform/` | Terraform definitions |
| `deploy/scripts/` | bootstrap, hot-add-node, health-check |

## Data flow, end to end

1. An operator (or CI via the SDK) submits a `FrameJob` — today through the
   REST API's simulation; at V1, persisted as a CR.
2. The operator's FrameJob controller renders an Argo `Workflow` and tracks it.
3. The Volcano/YuniKorn scheduler places the workload across GPU/RDMA nodes
   provisioned by Talos + Sidero.
4. Status flows back up: Workflow → FrameJob `.status` → API → UI.

## Why a single L2 site

The RDMA fabric (InfiniBand / RoCE) is a local interconnect; it does not
traverse WAN. `zones` and `racks` are failure-domain labels **within one
site**, not geographic regions. Federation across sites is a future,
separately-designed mechanism.
