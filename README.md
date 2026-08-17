# Frame — Mainframe Framework for Kubernetes

Frame turns a rack of bare-metal servers into a unified, self-healing, mainframe-grade computing platform: nine CRDs across two API groups — the eight frozen at `v1beta1` with `v1alpha1` still served and deprecated, plus `NodeTuning` added after the freeze — a React UI, and a TypeScript SDK for job orchestration, scheduling, resource management, node tuning, and observability.

> Single physical location only — multi-site federation is out of scope.

---

## How it works

```
UI / SDK / CI
    │  direct K8s API calls (/apis/frame.plume-labs.io/v1beta1/…)
    ▼
Frame CRDs  →  Frame operator  →  cluster primitives (Argo, PriorityClasses, Talos gRPC, …)
```

Dev: `kubectl proxy --port=8001`. Prod: ServiceAccount Bearer token via `window.__FRAME_TOKEN__`.

---

## Quick start

```bash
kubectl proxy --port=8001   # terminal 1
npm install && npm run dev  # terminal 2 → http://localhost:5173
```

```bash
make build && make run      # run the Go operator against your kubeconfig
```

→ See **[docs/getting-started.md](docs/getting-started.md)** for prerequisites, deploy-to-cluster, and troubleshooting.

---

## Documentation

| | |
|---|---|
| [Getting Started](docs/getting-started.md) | Prerequisites, local dev, first deploy |
| [Architecture](docs/architecture.md) | Three layers, data flows, topology constraints |
| [API & SDK](docs/api.md) | `FrameClient` SDK, auth, raw K8s API, RBAC |
| [CRD Reference](docs/crd-reference.md) | All nine CRDs across two API groups — fields, controllers, webhooks |
| [Runbook](docs/runbook.md) | Health checks, failover, certificates, backup/restore, node tuning |
| [Development](docs/development.md) | Build, test, lint — Go operator and React UI |
| [Deployment](docs/deployment.md) | Image build, kustomize overlays, cert-manager |
| [Upgrading](docs/upgrading.md) | Chart adoption, chart upgrades, and the `v1alpha1` → `v1beta1` migration |
| [Roadmap](docs/roadmap.md) | `v1beta1` beta → stable V1 |

---

## License

MIT — see [LICENSE](LICENSE).
