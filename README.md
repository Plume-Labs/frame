# Frame — Mainframe Framework for Kubernetes

You need big hardware to run big workloads with unified approach like mainframe do, but you don't have the money for a real mainframe ?

Frame turns a rack of bare-metal servers into a unified, self-healing, mainframe-like computing platform:
nine CRDs across two API groups — the eight frozen at `v1beta1` with `v1alpha1` still served and deprecated, plus `NodeTuning` added after the freeze — a React UI, and a TypeScript SDK for job orchestration, scheduling, resource management, node tuning, and observability.

> **Status: pre-release, heading for a public beta.** There is no tagged release yet and the API is still `v1beta1`. See [known limitations](docs/known-limitations.md) for what has and has not been proven.

> ⚠️ **Security notice.** Frame holds real authority over the cluster it manages, and its node-tuning agent runs as root-equivalent on every node. Do **not** expose the console to the Internet, and try Frame on a disposable cluster before anything that matters. Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

> Single physical location only — multi-site federation is out of scope for now.

---

## How it works

```
UI / SDK / CI
    │  direct K8s API calls (/apis/frame.plume-labs.io/v1beta1/…)
    ▼
Frame CRDs  →  Frame operator  →  cluster primitives (Argo, PriorityClasses, …)
```

Deployed: the console talks to the apiserver through the in-pod `frame-uiproxy` sidecar, which impersonates the signed-in user (passkey login via `authd`). Local development has its own loop: see [docs/development.md](docs/development.md).

---

## Quick start

```bash
make build && make run      # run the Go operator against your kubeconfig
```

The console needs the API proxy *and* `authd` reachable (two terminals plus the dev server). The exact loop is in **[docs/development.md](docs/development.md)**; **[docs/getting-started.md](docs/getting-started.md)** covers prerequisites, deploy-to-cluster, and troubleshooting.

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
| [Known limitations](docs/known-limitations.md) | What is unproven, unsupported or deliberately out of scope |

---

## License

MIT — see [LICENSE](LICENSE).
