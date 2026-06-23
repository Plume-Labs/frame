# Roadmap to V1 (stable)

Frame today is a **`v1alpha1` preview**: the operator reconciles six CRDs with
webhooks and envtest coverage, the IaC under `deploy/` provisions the cluster,
and the control-plane UI/API runs against an in-memory simulation. "V1 stable"
means: a frozen, conversion-guaranteed API; the UI/API driving real CRDs; the
controllers doing real work end-to-end; and a repeatable, secured release.

The bar for each phase is its **Exit criteria** — a phase is not done until
those are demonstrable, not just coded.

---

## Where we are (baseline)

- ✅ 6 CRDs in `frame.plume-labs.io/v1alpha1` with generated manifests + RBAC tiers
- ✅ Controllers with finalizers; FrameJob → ArgoWorkflow; FrameNode secondary
  watch on core `v1.Node`
- ✅ Validating/defaulting webhooks + envtest tests; e2e scaffold; CI workflows
- ✅ `deploy/` IaC: Talos, Sidero, PXE, Ceph, MinIO, Cilium/RDMA, Argo, monitoring, GitOps
- ✅ **SchedulingPolicy** reconciles into real `PriorityClass` + Volcano/YuniKorn
  queues (`PriorityValue`, `QueueWeight` fields; graceful degrade when CRD absent)
- ✅ **TalosMachineConfig** drives real `ApplyConfiguration` gRPC calls against the
  Talos endpoint (inline patch or ConfigMap ref; `ClientBuildFailed`/`ApplyFailed` conditions)
- ✅ **TalosUpgrade** drives real `Upgrade` gRPC with generation-based idempotency
  guard (no re-trigger on same spec generation)
- ✅ **FrameJob** full lifecycle parity: suspend/resume (`spec.suspended` →
  `Workflow.spec.suspend`), `Priority` → `priorityClassName`, `GPUCount` →
  `gpu-count` parameter, secondary watch on Argo Workflow
- ⚠️ REST API + UI back onto an in-memory simulation — **not** wired to the CRDs
- ⚠️ FrameResourceQuota validates but doesn't yet project into namespace `ResourceQuota`
  or scheduler limits
- ⚠️ No Kubernetes Events or Prometheus metrics emitted from controllers yet

---

## Phase 1 — Make the operator real (`v1alpha1`)

Close the gap between CRDs that *validate* and controllers that *act*.

- ✅ FrameJob: suspend/resume, `Priority` → `priorityClassName`, `GPUCount` →
  `gpu-count` param, secondary watch on Argo Workflow
- ✅ TalosMachineConfig: real `ApplyConfiguration` gRPC with inline + ConfigMap patch
- ✅ TalosUpgrade: real `Upgrade` gRPC with generation-based idempotency guard
- ✅ SchedulingPolicy: real `PriorityClass` + Volcano/YuniKorn queue reconciliation
- [ ] FrameResourceQuota: project into namespace `ResourceQuota` + scheduler queue limits
- ✅ Kubernetes Events from every controller (audit trail, `kubectl describe`)
- ✅ Prometheus custom metrics: `frame_framejob_{completed,failed}_total`,
  `frame_talosupgrade_{requested,alreadyatversion,failed}_total`,
  `frame_talosmachineconfig_{applied,failed}_total`,
  `frame_schedulingpolicy_applied_total`
- ✅ Envtest coverage threshold ≥ 45% on `internal/controller` tracked in CI

**Exit:** every CRD has an end-to-end envtest proving spec → real cluster effect →
status, green in CI.

---

## Phase 2 — Wire the control plane to the cluster

- ✅ Replace in-memory backend: `server/k8s.ts` wraps `@kubernetes/client-node`
  (`CustomObjectsApi`) for all 4 resource types. `loadFromDefault()` handles
  both kubeconfig (dev) and in-cluster SA (production). In-memory simulation
  remains as automatic fallback when no cluster config is found.
- ✅ Routes wired: `GET/POST/DELETE /api/jobs` → FrameJob CRs;
  `GET /api/nodes` + `GET /api/nodes/:id` → FrameNode CRs;
  `GET/POST/DELETE /api/scheduler/policies` → SchedulingPolicy CRs;
  `GET/PUT /api/resources/quotas` → FrameResourceQuota CRs.
- [ ] Authn/authz: map Bearer token to K8s ServiceAccount + RBAC tiers
  (currently single static `FRAME_API_TOKEN`, no per-user impersonation)
- [ ] SDK + OpenAPI regeneration from live API; contract tests
- [ ] SSE/Watch endpoint for real-time job phase updates to the UI

**Exit:** submitting a job in the UI creates a real `FrameJob` CR and the UI
reflects live status; `deploy/api/openapi.yaml` matches the running server.

---

## Phase 3 — API stabilization (`v1alpha1` → `v1beta1` → `v1`)

- [ ] Freeze field names/semantics; remove anything speculative
- [ ] Introduce `v1beta1` with a conversion webhook and a documented storage version
- [ ] Add CRD field validation (CEL / kubebuilder markers), printer columns, defaults
- [ ] Write a deprecation + migration policy; promote to `v1` once `v1beta1` is stable
- [ ] Lock RBAC tiers (viewer/editor/admin) to the final schema

**Exit:** `v1` is the storage version, conversion is tested round-trip, and a
documented upgrade path exists from any earlier alpha.

---

## Phase 4 — Production hardening & release

- [ ] Cert-manager-backed webhook TLS verified in-cluster (config already present)
- [ ] HA manager (leader election already wired) validated under failover
- [ ] Backup/restore (Velero) and checkpoint/IPMI-watchdog paths exercised in e2e
- [ ] Packaging: versioned image + Helm chart (and/or OLM bundle) with upgrade tests
- [ ] Full e2e on Kind in CI covering the happy paths of all six CRDs
- [ ] Security review: RBAC least-privilege, NetworkPolicies, image scanning, SBOM
- [ ] Docs: install guide, API reference, runbook, upgrade guide

**Exit:** a tagged `v1.0.0` installs from a published chart on a fresh cluster,
passes e2e, and survives a manager failover and an upgrade from the prior release.

---

## Explicitly post-V1 (not blocking)

- Multi-site / multi-region federation (WAN gateway / API aggregation / federation controller)
- Autoscaling of FrameNodes from spare bare metal
- Cost/capacity forecasting beyond the current dashboards
- Additional schedulers / accelerator vendors

---

## V1 definition of done

1. API is `v1` with tested conversion from alpha/beta.
2. UI + REST API + SDK drive real CRDs; no simulation in the default path.
3. All six controllers produce real, observable cluster effects with metrics + events.
4. One-command install from a published, versioned artifact.
5. CI is green across build, lint, unit/envtest, and Kind e2e.
6. Security and upgrade paths reviewed and documented.
