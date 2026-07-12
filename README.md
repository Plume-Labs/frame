# Frame — Mainframe Framework for Kubernetes

Frame turns a rack of bare-metal servers into a unified, self-healing, mainframe-grade computing platform: six CRDs, a React UI, and a TypeScript SDK for job orchestration, scheduling, resource management, and observability.

> Single physical location only — multi-site federation is out of scope.

---

## How it works

```
UI / SDK / CI
    │  direct K8s API calls (/apis/frame.plume-labs.io/v1alpha1/…)
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
| [CRD Reference](docs/crd-reference.md) | All six CRDs — fields, controllers, webhooks |
| [Development](docs/development.md) | Build, test, lint — Go operator and React UI |
| [Deployment](docs/deployment.md) | Image build, kustomize overlays, cert-manager |
| [Roadmap](docs/roadmap.md) | `v1alpha1` → stable V1 |

---

## License

MIT — see [LICENSE](LICENSE).
