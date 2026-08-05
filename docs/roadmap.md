# Roadmap to V1 (stable)

Frame is a **`v1alpha1` preview**: the operator reconciles six CRDs with webhooks and envtest coverage, the IaC under `deploy/` provisions the cluster, and the UI talks directly to the K8s CRD API. "V1 stable" means: a frozen, conversion-guaranteed API; all controllers producing real cluster effects end-to-end; and a repeatable, secured release.

The bar for each phase is its **Exit criteria** — a phase is not done until those are demonstrable, not just coded.

---

## Current state (baseline)

- ✅ 6 CRDs in `frame.plume-labs.io/v1alpha1` with generated manifests + RBAC tiers
- ✅ Controllers with finalizers; FrameJob → ArgoWorkflow; FrameNode secondary watch on core `v1.Node`
- ✅ Validating/defaulting webhooks + envtest tests; e2e scaffold; CI workflows
- ✅ `deploy/` IaC: Talos, Ceph (RGW), Cilium/RDMA, Argo, monitoring, GitOps
- 🚧 Bare-metal provisioning (`deploy/omni/`): manifests and install script ready, not deployed —
  Omni manages Talos machines and the test cluster is k3s, and there is no bare metal yet.
  Sidero Metal was dropped: upstream no longer develops it. `deploy/pxe/` is still only a README.
- ✅ UI talks directly to the Kubernetes CRD API (no Express server — `kubectl proxy` in dev, ServiceAccount token in prod)

---

## Phase 1 — Make the operator real (`v1alpha1`) ✅ DONE

- ✅ FrameJob: suspend/resume, `Priority` → `priorityClassName`, `GPUCount` → `gpu-count` param, secondary watch on Argo Workflow
- ✅ TalosMachineConfig: real `ApplyConfiguration` gRPC with inline + ConfigMap patch
- ✅ TalosUpgrade: real `Upgrade` gRPC with generation-based idempotency guard
- ✅ SchedulingPolicy: real `PriorityClass` + Volcano/YuniKorn queue reconciliation (graceful degrade when CRD absent)
- ✅ Kubernetes Events from every controller (audit trail, `kubectl describe`)
- ✅ Prometheus custom metrics: `frame_framejob_{completed,failed}_total`, `frame_talosupgrade_*`, `frame_talosmachineconfig_*`, `frame_schedulingpolicy_applied_total`
- ✅ Envtest coverage threshold ≥ 45% on `internal/controller` tracked in CI
- [ ] FrameResourceQuota: project into namespace `ResourceQuota` + scheduler queue limits

**Exit:** every CRD has an end-to-end envtest proving spec → real cluster effect → status, green in CI.

---

## Phase 2 — Wire the control plane to the cluster ✅ DONE

- ✅ UI talks directly to the Kubernetes API (`/apis/frame.plume-labs.io/v1alpha1/…`)
- ✅ Express server removed; no intermediate REST layer
- ✅ Dev: `kubectl proxy --port=8001` + Vite proxy on `/apis`
- ✅ Prod: `window.__FRAME_TOKEN__` ServiceAccount Bearer token
- ✅ All six CRD kinds readable/writable from the UI and SDK
- [ ] Authn/authz: per-user K8s impersonation (currently single SA token, no per-user RBAC mapping)
- [ ] SSE/Watch endpoint for real-time job phase updates (currently polling)
- [ ] SDK + OpenAPI contract tests

**Exit:** submitting a job in the UI creates a real `FrameJob` CR and the UI reflects live status.

---

## Phase 3 — API stabilization (`v1alpha1` → `v1beta1` → `v1`)

- [ ] Freeze field names/semantics; remove speculative fields
- [ ] Introduce `v1beta1` with a conversion webhook and documented storage version
- [ ] Add CRD field validation (CEL / kubebuilder markers), printer columns, defaults
- [ ] Write a deprecation + migration policy; promote to `v1` once `v1beta1` is stable
- [ ] Lock RBAC tiers (viewer/editor/admin) to the final schema

**Exit:** `v1` is the storage version, conversion is tested round-trip, and a documented upgrade path exists from any earlier alpha.

---

## Phase 4 — Production hardening & release

- [ ] Cert-manager-backed webhook TLS verified in-cluster
- [ ] HA manager (leader election already wired) validated under failover
- [ ] Backup/restore (Velero) and checkpoint/IPMI-watchdog paths exercised in e2e
- [ ] Packaging: versioned image + Helm chart (and/or OLM bundle) with upgrade tests
- [ ] Full e2e on Kind in CI covering all six CRDs
- [ ] Security review: RBAC least-privilege, NetworkPolicies, image scanning, SBOM
- [ ] Docs: install guide, API reference, runbook, upgrade guide

**Exit:** a tagged `v1.0.0` installs from a published chart on a fresh cluster, passes e2e, and survives a manager failover and an upgrade from the prior release.

---

## Explicitly post-V1

- Multi-site / multi-region federation (WAN gateway / API aggregation / federation controller)
- Per-user K8s impersonation from the UI (full RBAC passthrough)
- SSE/Watch real-time status stream
- Autoscaling of FrameNodes from spare bare metal
- Cost/capacity forecasting beyond current dashboards

---

## V1 definition of done

1. API is `v1` with tested conversion from alpha/beta.
2. UI + SDK drive real CRDs; no simulation in the default path.
3. All six controllers produce real, observable cluster effects with metrics + events.
4. One-command install from a published, versioned artifact.
5. CI is green across build, lint, unit/envtest, and Kind e2e.
6. Security and upgrade paths reviewed and documented.
