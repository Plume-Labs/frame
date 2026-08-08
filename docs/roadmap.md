# Roadmap

Frame is a **`v1alpha1` preview**: the operator reconciles seven CRDs with webhooks and envtest coverage, the IaC under `deploy/` provisions the cluster, and the UI talks directly to the Kubernetes CRD API.

Two things advance at once:

- **The V1 path** — clear the debt, go real-time, freeze the current API group, package it, release it. This is a narrowing exercise: no new capability enters V1.
- **New API groups** — a service catalog, SDN management, auto-update, and application management. Each lives in its own API group at `v1alpha1` so it can move without blocking the V1 freeze.

They are not fully independent. **The service catalog (S1) comes before the freeze**, because a catalog is the most likely thing to prove the core API wrong — service instances have to be counted by FrameResourceQuota, their workloads have to reach SchedulingPolicy, and FrameApplication has to bind to them. Freezing before learning that buys a conversion to v2 later.

The gate is narrow on purpose: the freeze waits for S1's *model* to settle, proven by one service type, not for all four types to ship. Otherwise V1 waits on KubeVirt.

Running both tracks with a single operator means alternating between them, not advancing both at once. Whichever gets attention, the other waits.

The bar for each phase is its **Exit criteria** — a phase is not done until those are demonstrable, not just coded.

---

## Current state (baseline)

- ✅ 7 CRDs in `frame.plume-labs.io/v1alpha1`: FrameJob, FrameNode, FrameResourceQuota, FrameUser, SchedulingPolicy, TalosMachineConfig, TalosUpgrade — with generated manifests and RBAC tiers
- ✅ Every CRD has a controller producing real cluster effects, with finalizers, Kubernetes Events and Prometheus metrics
- ✅ Validating/defaulting webhooks + envtest coverage threshold ≥ 45% on `internal/controller`, tracked in CI
- ✅ UI (13 tabbed screens behind an Overview landing) and SDK talk directly to the Kubernetes API — no intermediate server. Dev: `kubectl proxy`. Prod: ServiceAccount Bearer token.
- ✅ `deploy/` IaC: Talos, Ceph (RGW), Cilium/RDMA, Argo, monitoring, GitOps
- 🚧 **authd is built and deployed but inert.** Argon2id passwords, ES256 OIDC issuer with JWKS, usernameless WebAuthn, FrameUser store with a last-admin webhook guard — all merged and running in `cluster-control`, consumed by nothing. Stage 2 (configuring the apiserver to trust it) and Stage 3 (switching the UI over) are unwritten.
- 🚧 Bare-metal provisioning (`deploy/omni/`): manifests and install script ready, never executed — Omni manages Talos machines, the test cluster is k3s, and there is no bare metal yet. Sidero Metal was dropped: upstream no longer develops it. `deploy/pxe/` is still only a README.

---

## The V1 path

V1 means: a frozen, conversion-guaranteed API for `frame.plume-labs.io`; live status instead of polling; and a repeatable, secured release. It does **not** mean per-user authentication — see the RBAC note in Phase B.

### Phase A — Clear the debt ✅ scoped

Nothing here is new capability. It removes claims the code no longer backs, and proves the claims it does.

- [x] Delete `deploy/api/openapi.yaml`. It documented a REST API on `localhost:4000` served by an Express server that was removed. Keeping a spec for an API that does not exist is worse than having no spec. `PRD.md` described the same absent server and was corrected with it — the CRDs are the API, and their OpenAPI schema is served by the apiserver from the kubebuilder markers.
- [ ] Decide FrameResourceQuota's scheduler queue limits. **Proposal: cut them.** The controller already projects `corev1.ResourceQuota` into every matching namespace; `SchedulingPolicy` already reconciles Volcano/YuniKorn queues. Duplicating quota into queue limits is a second source of truth for the same number, and no caller has asked for it.
- [ ] Extend the Kind e2e suite to cover each CRD. It currently covers manager startup, the metrics endpoint, cert-manager provisioning and webhook CA injection — no CRD is exercised end-to-end in CI.
- [ ] Document FrameUser and correct the CRD count. `crd-reference.md` describes six CRDs and omits FrameUser entirely; `README.md`, `docs/README.md`, `architecture.md`, `api.md` and `deployment.md` all say "six" on the strength of it. Write the reference entry first, then fix the count — renaming six to seven while the seventh is undocumented only moves the lie.

**Exit:** no document describes an API that does not exist, and every CRD is proven spec → cluster effect → status on Kind in CI.

### Phase B — Freeze the API, lock RBAC

**Gated on S1.** Do not start this until the service catalog's model is settled and proven by the inference type, and any core-API changes it forces have landed. See S1 under "New API groups" below.

- [ ] Freeze field names and semantics; remove speculative fields
- [ ] Fold in whatever S1 proved the core needs — quota accounting for service instances, scheduling for service workloads, the binding surface applications consume
- [ ] Introduce `v1beta1` with a conversion webhook and a documented storage version
- [ ] Add CRD field validation (CEL / kubebuilder markers), printer columns, defaults
- [ ] Write a deprecation and migration policy; promote to `v1` once `v1beta1` is stable
- [ ] Lock the viewer/editor/admin RBAC tiers to the final schema, and extend them to the new API groups as those land

**On what RBAC means in V1.** The tiers exist as manifests, but the UI authenticates with a single ServiceAccount token — so the tiers are not currently enforced against any human. V1 delivers *correct, frozen, tested tiers*. Enforcing them per user requires authd Stages 2 and 3, which are post-V1. V1 documentation must say this plainly rather than advertise RBAC it does not enforce.

**Exit:** `v1` is the storage version, conversion is tested round-trip, and a documented upgrade path exists from any earlier alpha.

### Phase C — Real-time

Runs in parallel with Phase B: it changes the SDK surface, not the CRD schemas.

The previous roadmap called for an "SSE/Watch endpoint". There is no server left to host one. The apiserver the UI already talks to serves watch streams natively (`?watch=true`, chunked), so this is a client change with nothing new to deploy.

- [ ] Replace polling with apiserver watch streams in `frame-sdk`
- [ ] Handle `resourceVersion` expiry with a re-list, reconnect with backoff, and degrade to polling if the watch cannot be established
- [ ] Convert the screens that change fastest first: FrameJobs (phase transitions), FrameNodes, Events

**Exit:** a FrameJob phase transition appears in the UI with no poll interval in between, and killing the stream mid-flight recovers on its own.

### Phase D — Production hardening and release

- [ ] Packaging: versioned image + Helm chart with upgrade tests. **No chart exists today** — deployment is kustomize overlays only.
- [ ] Cert-manager-backed webhook TLS verified in-cluster
- [ ] HA manager (leader election already wired) validated under failover
- [ ] Backup/restore (Velero) and checkpoint/IPMI-watchdog paths exercised in e2e
- [ ] Security review: RBAC least-privilege, NetworkPolicies, image scanning, SBOM
- [ ] Docs: install guide, API reference, runbook, upgrade guide

**Exit:** a tagged `v1.0.0` installs from a published chart on a fresh cluster, passes e2e, and survives a manager failover and an upgrade from the prior release.

---

## New API groups

Four capabilities that Frame does not have. Each gets its own API group at `v1alpha1`, versioned independently of `frame.plume-labs.io`, so building them never blocks the V1 freeze. Each gets its own spec before any code.

Four of these requests share one shape — *declare a desired resource, a controller provisions it, a consumer binds to it*. Databases, queues, inference servers and VMs are not four projects; they are four types in one catalog.

### S1 — Service catalog (`services.frame.plume-labs.io/v1alpha1`)

Declarative provisioning of service instances with a lifecycle and credential binding: **inference, database, queue, VM**.

**This is the one new group that precedes the V1 freeze**, and it is sliced so that only its first part does:

1. **The model + inference** — the instance/binding shape, and one type implemented against it. Today `InferenceView` reads llama.cpp metrics from Prometheus — monitoring only, no provisioning. Managing inference means declaring a model server and having Frame stand it up. This part gates Phase B: whatever it proves the core API needs must land before the freeze.
2. **Database, queue, VM** — further types on a settled model. These do not gate anything. VM implies KubeVirt, which appears nowhere in the repo: greenfield, and the reason V1 must not wait for the full catalog.

Note the hardware constraint on the inference type: the current GPU is a Pascal P4 (`sm_6.1`), which rules out vLLM and KubeAI. `deploy/caching/vllm-rdma-kvcache.yaml` exists but cannot run here. llama.cpp is the only viable backend until the hardware changes — the model must not assume otherwise.

### S2 — SDN (`net.frame.plume-labs.io/v1alpha1`)

`deploy/networking/` is static YAML — Cilium, Multus, SR-IOV, RDMA device plugin, PTP, DPDK — applied once, with no management surface. S2 turns segmentation, policies and network attachments into declared resources a controller reconciles.

### S3 — Auto-update

Frame itself and its components. `TalosUpgrade` already covers the node OS; nothing covers the operator, the UI, authd, or the addons under `deploy/`.

### S4 — Application management

Three layers of one subsystem:

1. **`FrameApplication` CRD** — the model. An application declares its components, the services it requires (binding to S1), its quotas and its scheduling policy; a controller reconciles it.
2. **Deploy from Frame** — the action. Install and update an application (Helm chart or manifests) from the UI, with Frame owning the lifecycle.
3. **A grouped view** — the surface. Today `ApplicationsView` lists Deployments read through the SDK. Group by application rather than by Deployment, with health, logs and rollback.

Layer 3 ships on its own, immediately: it is UI grouping over reads that already work, and needs neither the CRD nor S1. Layers 1 and 2 follow S1, since an application binding to services requires services to exist.

### Phase E — Stabilize the new groups

The same ritual as Phase B — conversion webhook, CEL validation, printer columns, RBAC tiers — applied to S1–S4 once each surface has settled. Without this, the new groups stay alpha indefinitely and the point of separating them is lost.

---

## V2

**Multi-site / multi-region federation.** Note that `PRD.md` currently declares this out of scope — "Frame manages a single local cluster", with an RDMA fabric that is local by construction and does not traverse the WAN. V2 will have to contradict the PRD; that argument belongs in the V2 spec, not here.

---

## Post-V1, unscheduled

- authd Stages 2 and 3 — per-user authentication and enforced RBAC
- `deploy/omni` execution — blocked on hardware: no bare metal, and the test cluster is k3s
- `deploy/pxe` — README only
- Autoscaling FrameNodes from spare bare metal
- Cost/capacity forecasting beyond the current dashboards

---

## V1 definition of done

1. `frame.plume-labs.io` is `v1` with tested conversion from alpha and beta.
2. UI and SDK drive real CRDs over watch streams; no polling and no simulation in the default path.
3. All seven controllers produce real, observable cluster effects with metrics and events, each proven end-to-end on Kind in CI.
4. One-command install from a published, versioned Helm chart.
5. CI is green across build, lint, unit/envtest, and Kind e2e.
6. Security and upgrade paths reviewed and documented, including an explicit statement that RBAC tiers are not yet enforced per user.
