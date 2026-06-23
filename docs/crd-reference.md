# CRD Reference

API group `frame.plume-labs.io`, version `v1alpha1`, all namespaced. Generated
CRDs live in `config/crd/bases/`; sample CRs in `config/samples/`. Each kind has
a controller (`internal/controller/`) and a webhook (`internal/webhook/v1alpha1/`).

> `v1alpha1` means the schema may change without conversion guarantees. See
> [roadmap.md](roadmap.md) for the path to a stable API.

---

## FrameNode

A bare-metal machine Frame manages. Bridges a physical/Talos node to its
Kubernetes `v1.Node`.

**Spec:** `ip`, `role`, `network` (`address`, `gateway`, `dns[]`, optional
`vlan`, `bond`), `disk`, `rack`, `zone`, `serviceClass`, optional
`rdmaInterface`, `hostname`, `serverClassRef`.

**Status:** `phase`, `conditions[]`, `talosVersion`, `kubeletVersion`,
`lastHeartbeat`, `capacity`, `allocatable`, `nodeName`, `providerID`.

**Controller:** finalizer-guarded; secondary-watches core `v1.Node` and maps it
back to its FrameNode (`nodeToFrameNode`) to keep phase/versions in sync.

---

## FrameJob

A workload submitted to the cluster, realized as an Argo `Workflow`.

**Spec:** `name`, `pipeline`, optional `serviceClass`, `priority`
(critical/high/medium/low), `namespace`, `gpuCount`, `parameters` (map),
`suspended` (bool, default false).

**Status:** `phase` (Pending/Submitted/Running/Suspended/Completed/Failed),
`conditions[]`, `argoWorkflowName`, `startTime`, `completionTime`, `message`.

**Controller:**
- On create: builds an Argo `Workflow` with `spec.suspend`, `priorityClassName`
  (mapped from `priority` → `frame-{priority}`), and arguments `gpu-count` +
  `service-class`; sets `Submitted` condition.
- On update: syncs `spec.suspended` → `Workflow.spec.suspend` via patch;
  derives `phase` from workflow status (Argo `Succeeded` → `Completed`, etc.);
  surfaces `Suspended` when `spec.suspended=true` and workflow isn't terminal.
- Secondary watch on Argo `Workflow` objects (label `frame.plume-labs.io/job` +
  `frame.plume-labs.io/job-namespace`) — reacts to Workflow changes instead of
  polling every 30 s.
- On delete: removes the Workflow via finalizer.

---

## SchedulingPolicy

Queue / priority configuration for the HPC scheduler.

**Spec:** `scheduler` (volcano/yunikorn/…), optional `queueName`,
`priorityClass`, `gangScheduling`, `preemption`, `priorityValue` (int32,
default 0), `queueWeight` (int32, min 1, default 1).

**Status:** `conditions[]`.

**Controller:**
- Reconciles a cluster-scoped `PriorityClass` named by `spec.priorityClass`
  with `value=spec.priorityValue` and `preemptionPolicy` derived from
  `spec.preemption`. Uses two labels (`frame.plume-labs.io/policy-namespace` +
  `frame.plume-labs.io/policy-name`) to track ownership across scope boundaries.
- When `scheduler=volcano`: reconciles a `scheduling.volcano.sh/v1beta1/Queue`
  with `weight` and `reclaimable`.
- When `scheduler=yunikorn`: reconciles a `yunikorn.apache.org/v1alpha1/Queue`
  with `weight` and `preemption.allowPreemptSelf`.
- Missing scheduler CRD → `Ready=False` with reason `QueueCRDMissing` (graceful
  degrade, no retry storm). Missing PriorityClass CRD → hard fail + retry.
- Cleanup on delete via finalizer (removes PriorityClass + queue).

---

## FrameResourceQuota

Per-service-class resource ceiling.

**Spec:** `serviceClass`, optional `maxGPUs`, `maxCPU` (Quantity),
`maxMemory` (Quantity), `maxJobs`.

**Status:** `conditions[]`.

---

## TalosMachineConfig

Declarative Talos MachineConfig application to a node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, and one of
`configPatch` (inline YAML) or `configPatchRef` (ConfigMap key selector).

**Status:** `conditions[]` (Ready=True reason `Applied`; False reasons:
`PatchResolveFailed`, `ClientBuildFailed`, `ApplyFailed`).

**Controller:** reads TLS credentials from the referenced Secret (keys
`ca`/`ca.crt`, `crt`/`tls.crt`, `key`/`tls.key`), resolves the patch,
calls `ApplyConfiguration` gRPC with `mode=AUTO`, updates condition. Retries
with 30 s backoff on transient failures. Uses a finalizer for clean deletion.

---

## TalosUpgrade

A Talos OS upgrade for a single node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, `image`, optional
`preserveData`.

**Status:** `conditions[]` (Ready=True reasons: `UpgradeRequested`,
`AlreadyAtVersion`; False reasons: `ClientBuildFailed`, `UpgradeFailed`).

**Controller:** generation-based idempotency guard — only calls `Upgrade` gRPC
when the `Ready` condition's `observedGeneration` differs from `.metadata.generation`,
preventing re-trigger while the node is rebooting. Uses `stage=false, force=false`.
Retries with 30 s backoff on transient failures.

---

## Webhooks

FrameNode and FrameJob have **defaulting + validation**; the other four have
**validation** only (per `PROJECT`). Validators enforce required fields and
value ranges before a CR is admitted. Tests: `internal/webhook/v1alpha1/*_test.go`.
