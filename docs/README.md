# Frame Documentation

Frame turns a rack of bare-metal servers into a single, self-healing,
mainframe-grade Kubernetes platform. This folder is the engineering
documentation; the top-level [`README.md`](../README.md) is the product
overview and quick start.

## Contents

| Doc | What it covers |
|---|---|
| [architecture.md](architecture.md) | The three layers (control plane, operator, IaC) and how they fit together |
| [crd-reference.md](crd-reference.md) | The six `frame.plume-labs.io/v1alpha1` CRDs — fields, controllers, webhooks |
| [development.md](development.md) | Build, test, run, and deploy the Go operator locally |
| [roadmap.md](roadmap.md) | Path from the current `v1alpha1` preview to a stable **V1** release |

## The 30-second model

```
operator / CI / UI
        │  REST + TypeScript SDK
        ▼
Frame control plane (React UI + Express API)     ← src/, server/
        │  Kubernetes CRs
        ▼
Frame operator (controllers + webhooks)          ← api/, internal/
        │  reconciles into
        ▼
Cluster primitives (ArgoWorkflows, core Nodes,   ← deploy/
ResourceQuotas, Talos machines, …)
```

- **Control plane** — what humans and pipelines talk to. Today it serves an
  in-memory simulation with a REST API + SDK; wiring it to the live operator
  CRDs is a V1 goal (see [roadmap](roadmap.md)).
- **Operator** — Kubernetes-native reconcilers for the six Frame CRDs. This is
  the part that actually changes cluster state.
- **IaC** (`deploy/`) — Talos + Sidero provisioning, Ceph/MinIO storage, Cilium
  RDMA networking, GitOps, and Argo Workflows manifests.

## Scope

Frame manages a **single local cluster** — one physical location, one or more
racks sharing an L2 segment for RDMA. Multi-site / multi-region federation is
intentionally out of scope for this version.
