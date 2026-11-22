# Hearthrack — Kubernetes for AI & HPC.

You need big hardware to run big workloads with unified approach like mainframe do, but you don't have the money for a real mainframe ?

Hearthrack turns a rack of bare-metal servers into a unified, self-healing, mainframe-like computing platform:
19 CRDs across two API groups (`frame.plume-labs.io`, `services.plume-labs.io`) — all at `v1beta1`, with a deprecated `v1alpha1` still served on the six oldest — a React UI, and a TypeScript SDK for job orchestration, scheduling, resource management, node tuning, and observability.

> **Hearthrack was called Frame until 2026-11.** The API group (`frame.plume-labs.io`), the `Frame*` kinds, namespaces and Helm chart name keep the old name on purpose, so existing installs keep working. See [naming.md](docs/naming.md) for what stays and why.

> **Status: pre-release.** There is no tagged release yet and the API is still `v1beta1`. See [known limitations](docs/known-limitations.md) for what has and has not been proven.

> ⚠️ **Security notice.** Hearthrack holds real authority over the cluster it manages, and its node-tuning agent runs as root-equivalent on every node. Do **not** expose the console to the Internet, and try Hearthrack on a disposable cluster before anything that matters. Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

> Single physical location only — multi-site federation is out of scope for now.

---

## How it works

```
UI / SDK / CI
    │  direct K8s API calls (/apis/frame.plume-labs.io/v1beta1/…)
    ▼
Hearthrack CRDs  →  Hearthrack operator  →  cluster primitives (Argo, PriorityClasses, …)
```

Deployed: the console talks to the apiserver through the in-pod `frame-uiproxy` sidecar, which impersonates the signed-in user (passkey login via `authd`). Local development has its own loop: see [docs/development.md](docs/development.md).

---

## Try it without hardware

```bash
git clone https://github.com/Plume-Labs/frame && cd frame
make demo        # ~7 min the first time: a throwaway Kind cluster with the whole stack
make demo-down   # delete it
```

You need docker, [kind](https://kind.sigs.k8s.io), kubectl, Go, curl and openssl. The demo cluster has its **own kubeconfig**: your current kubectl context is never touched. It gives you the real operator, authd and console on four demo nodes in two racks. Sign-in is by **passkey** (no password): the command prints the one-time step. A few container jobs, two volumes, two alerts and the four scheduling tiers are seeded as real objects labelled `hearthrack.demo/seeded`; there is no real hardware behind it, and no Argo pipelines or Ceph: see [known limitations](docs/known-limitations.md#the-demo-make-demo). A CI job runs this exact path on every push, so this section cannot silently rot.

---

## Quick start (development)

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
| [CRD Reference](docs/crd-reference.md) | All 19 CRDs across two API groups — fields, controllers, webhooks |
| [Runbook](docs/runbook.md) | Health checks, failover, certificates, backup/restore, node tuning |
| [Development](docs/development.md) | Build, test, lint — Go operator and React UI |
| [Deployment](docs/deployment.md) | Image build, kustomize overlays, cert-manager |
| [Upgrading](docs/upgrading.md) | Chart adoption, chart upgrades, and the `v1alpha1` → `v1beta1` migration |
| [Roadmap](docs/roadmap.md) | `v1beta1` beta → stable V1 |
| [Naming](docs/naming.md) | Hearthrack vs. the `frame` names that remain in the API and on disk |
| [Known limitations](docs/known-limitations.md) | What is unproven, unsupported or deliberately out of scope |

---

## How it is built

Much of this code is written with AI assistance (Claude), and the commits say so
with a `Co-Authored-By` trailer. It is held to the same bar as any other change:
tests, the CI jobs listed in [CONTRIBUTING.md](CONTRIBUTING.md), and a human
maintainer accountable for what merges.

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). Contributions are accepted under the [Developer Certificate of Origin](CONTRIBUTING.md#sign-your-commits-dco).
