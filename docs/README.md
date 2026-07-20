# Frame Documentation

Engineering documentation for the Frame operator and control plane. For the product overview and quick start, see the top-level [README](../README.md).

## Contents

| Doc | What it covers |
|---|---|
| [getting-started.md](getting-started.md) | Prerequisites, local dev loop, first deploy — start here |
| [architecture.md](architecture.md) | Three layers (control plane, operator, IaC) and how they fit together |
| [api.md](api.md) | CRD API, TypeScript SDK (`FrameClient`), authentication |
| [crd-reference.md](crd-reference.md) | All six `frame.plume-labs.io/v1alpha1` CRDs — fields, controllers, webhooks |
| [development.md](development.md) | Build, test, lint, run — Go operator and React UI |
| [deployment.md](deployment.md) | Build image, kustomize overlays, in-cluster auth, cert-manager |
| [roadmap.md](roadmap.md) | Path from `v1alpha1` preview to stable V1 |

## 30-second model

```
Operator / CI / UI (TypeScript SDK)
        │  reads/writes Kubernetes CRs directly
        ▼
Frame CRDs (frame.plume-labs.io/v1alpha1)
        │
        ▼
Frame operator (controllers + webhooks)   ← api/, internal/, cmd/
        │  reconciles into
        ▼
Cluster primitives (ArgoWorkflows, PriorityClasses, Talos gRPC, …)
        │
        ▼
Bare-metal IaC (deploy/)
```

- **Control plane** (`src/`) — React UI + TypeScript SDK. Both talk **directly to the Kubernetes API** — no intermediate server. Dev: `kubectl proxy`. Prod: ServiceAccount Bearer token.
- **Operator** (`internal/`) — Kubebuilder v4 controllers for six CRDs. This is the layer that actually mutates cluster state.
- **IaC** (`deploy/`) — Talos + Sidero provisioning, Ceph (RGW) storage, Cilium RDMA networking, GitOps, and Argo Workflows manifests.

## Scope

Frame manages a **single local cluster** — one physical location, one or more racks sharing an L2 segment for RDMA. Multi-site / multi-region federation is out of scope for this version.
