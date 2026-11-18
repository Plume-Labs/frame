# CRD Reference

19 CRDs across two API groups: 17 in `frame.plume-labs.io` and
`FrameService` plus `InferenceModel` in `services.plume-labs.io` — a separate
group so the service catalog can move without blocking the `frame.plume-labs.io`
freeze (see [roadmap.md](roadmap.md)). Generated CRDs live in
`config/crd/bases/`; sample CRs in `config/samples/`. Most kinds have a
controller (`internal/controller/<group>/`, plus `internal/alerting/`); FrameUser
and FrameTask have none. Webhooks live in `internal/webhook/<group>/v1beta1/`.

The text below was written when nine kinds existed, and the paragraph that
follows describes that generation.

Six of those nine are namespaced and serve both **`v1beta1`** (storage) and
a deprecated `v1alpha1`. **NodeTuning, FrameTask and FrameMachine are the
exceptions**: all three postdate the freeze, so all three have **no
`v1alpha1`** (nothing to convert from) and **no webhook**. NodeTuning is
additionally **cluster-scoped**; FrameTask and FrameMachine are namespaced,
like the six frozen kinds. NodeTuning and FrameTask also report status as
a `phase` rather than a `Ready` condition — see each one's section for why
those are two different reasons, and read both as documented divergences,
not a licence to add more. **FrameMachine is not a third instance of that
divergence**: it has a controller and reports health through its own
condition type — see its section below for why that type is `Reachable`
rather than the shared `Ready`.

> This page documents **`v1beta1`**, the storage version and the conversion
> hub. What the freeze does and does not promise, the nine differences from
> `v1alpha1`, and the deprecation policy are in
> [upgrading.md](upgrading.md), "API versions and the migration path". A
> `v1alpha1` client is still served and still correct; it gets a deprecation
> warning naming what changed for that kind.

---

### `status.observedGeneration`

Every long-lived kind except `FrameTask` carries a top-level
`status.observedGeneration` (the one-shot acts FrameDiskClaim,
FrameOSDReplacement and FrameCephAction do not, and neither does the
receiver-written FrameAlert):
the `metadata.generation` its status was computed from. Compare it to
`metadata.generation` to tell whether the controller has seen the current
spec. Conditions carry their own per-condition `observedGeneration` as well;
the top-level field is the one a client can read without knowing which
condition types this kind writes. `FrameTask` has neither: nothing ever
reconciles its `spec` against a later `spec`, so there is no "has the
controller seen the current generation" question to answer — `spec` is
written once and never patched, and `status` is the outcome of that one
call, not a convergence against it. `FrameUser` has the field and no writer —
it has no controller.

### No top-level `status.phase`

> **Exceptions added since the rule below was written.** FrameInstall,
> FrameDiskClaim, FrameOSDReplacement and FrameCephAction are **one-shot acts**,
> not states to converge to: each runs once through a linear lifecycle and stops
> (`Ready`/`Failed`, `Ready`/`Failed`, `Succeeded`/`Cancelled`/`Failed`,
> `Done`/`Refused`/`Failed` respectively). Like FrameTask they report a `phase`;
> unlike it, a controller drives them. FrameStorage carries a `phase`
> (`Ready`/`Degraded`/`Unknown`) as a health summary the console reads.
> NodeTuning, below, reports one per node. FrameAlert reports a `state`
> (`Firing`/`Resolved`) and FrameAlertSubscription no phase at all. These are
> documented divergences, not a licence: the rule still stands for any new kind
> that describes a state rather than a one-shot act.

When this was written no kind had a top-level one. Health is reported through
`status.conditions`, and every kind that describes a state and has a controller
writes a `Ready` condition — with two exceptions, recorded below, which are
exceptions for two different reasons, plus the one-shot acts in the note above,
which report through `phase`.

This is a rule, not a drift. A single enum forces the API to pick one
dimension of health out of several and cannot express "provisioned but
degraded", which is why the Kubernetes API conventions have called `phase`
strongly discouraged for new APIs since 2019. **Do not add a `status.phase`
field to a Hearthrack kind.** If a lifecycle needs more than `Ready`, add a second
condition type and document its reason vocabulary here.

**NodeTuning is the exception, and it is a partial one.** It has no top-level
`status.phase` either, and it does carry `status.conditions` — but nothing
writes that field today. What it reports instead is a `phase` *per node*, in
`status.nodes[].phase`, because the object describes N nodes at once and a
single cluster-wide `Ready` would have to collapse "two nodes in sync, one
awaiting approval, one failed" into one boolean. A per-node condition array
would have been the conventional answer; a per-node enum is what shipped.
Clients reading NodeTuning must branch on `status.nodes[].phase` and
`status.nodes[].realization`, not on conditions.

**FrameTask is the other exception, and for a different reason: it is not
reconciled at all.** It has no controller, so "add a second condition type"
above does not apply to it the way it would to a reconciled kind — there is
no desired state in `spec` for anything to converge toward, only a record of
one HTTP call the proxy already made and is now reporting the outcome of.
`status.phase` (`Running` → `Succeeded`/`Failed`, written directly by
`internal/uiproxy/recorder.go` as the call starts and finishes) is exactly
the right shape for that: a finished HTTP call has exactly one dimension of
health, not several that could disagree, which is the situation `Ready`
conditions exist to express and a `FrameTask` never has. Reusing `Ready`
here would invent a distinction — "ready" as opposed to what? — that the
object has no second axis to hold.

Three kinds — FrameJob, FrameNode, FrameService — had one at `v1alpha1`, and
that version still serves it: it is computed out of the conditions on the way
down and is never stored. The `PHASE` column in `kubectl get framejobs` and
`kubectl get framenodes` survives on `v1beta1` by reading the `Ready`
condition's `reason` directly.

`Ready.reason` is therefore part of the frozen contract, because clients
branch on it:

| Kind | `Ready.reason` vocabulary |
|---|---|
| FrameJob | `Submitted`, `Running`, `Suspended`, `Completed`, `Failed`. `status` is `True` only on `Completed`. |
| FrameNode | `Online`, `Degraded`, `Offline` once the node is in the cluster; `AwaitingNode` while no Kubernetes Node carries its name; `Unclassified` when it may not write labels (see below). `Discovered` and `Provisioning` were written by the retired Talos path and may linger on an object until its next reconcile. `status` is `True` only on `Online`. |
| FrameResourceQuota | `Reconciled` on success. A reconcile that fails returns an error and requeues rather than writing a failure reason. |
| SchedulingPolicy | `Applied` on success, `ReconcileError` otherwise (a missing scheduler Queue CRD degrades here rather than hard-failing, and also emits a `QueueCRDMissing` Event). |
| FrameService | diagnostic, not a lifecycle: `Reconciled`, `UnknownType`, `NotProvisionable`, `SizeRefused`, `ModelCacheMissing`, and whatever else the provider returns. Read `status`, not `reason`. |
| FrameUser | none — it has no controller. |
| FrameTask | not applicable — it has no `Ready` condition at all; read `status.phase` (`Running`, `Succeeded`, `Failed`) instead, see below. |

> **FrameMachine also writes four storage conditions:** `StorageDegraded`,
> `ScrubErrors`, `StorageCapacityHigh` and `DiskWearHigh`. Reason `Detected`
> or `LastReadHeld` when `True`, `Healthy` when `False`, `ReadUnavailable` or
> `WearNotReported` when `Unknown`. `Unknown` means not read, never healthy.
> See [storage.md](storage.md#storage-health-alerts). `NetworkDegraded`,
> `LinkSpeedLow`, `LinkSaturated` and `LinkErrorsRising` follow the same vocabulary,
> see [network.md](network.md).

> **FrameMachine writes a condition, but not this one.** Its controller
> writes `Reachable`, not `Ready` — a deliberate choice (`conditionReachable`
> in `internal/controller/frame/framemachine_controller.go`): a chassis can
> answer its BMC while powered off, which is not a state any other
> controller in this table would call `Ready`. `Reachable.reason` is
> `Probed` on success; `TLSError`, `AuthFailed`, `Timeout`,
> `ConnectionRefused`, `Unsupported`, `CredentialsUnavailable` or
> `ProbeFailed` otherwise — see FrameMachine's
> own section below and [runbook.md](runbook.md) for what each means.

> **Enum members no controller ever wrote (R6).** `v1alpha1`'s `phase` enums
> were wider than anything that ever populated them. FrameJob's `Pending` is
> now *reachable* — it is what an object with no conditions at all projects
> to. FrameNode's `Discovering` and `Failed`, and FrameService's
> `Provisioning`, are not: nothing ever wrote them, so the projection
> declines to invent a source. FrameNode projects an unrecognised or absent
> reason to the empty string (an absent field, which is what an unreconciled
> node always looked like) rather than guessing `Degraded`, which is an
> actionable hardware claim that invites draining a healthy node.
>
> That is *missing controller behaviour, not dead schema*. Adding those
> states later is a controller change with no API impact at all, because a
> condition `reason` is a free string — which is exactly the property the
> enum did not have.

---

## FrameNode

How Hearthrack classifies one Kubernetes `v1.Node`: role, rack, zone, service
class and RDMA, projected onto the node as labels. It does not install
anything — installing a machine is `FrameInstall`'s job, and the hardware
itself is a `FrameMachine`. (Until 2026-09-27 FrameNode also carried a Talos
provisioning half — `spec.disk`, `status.discovered*`, a maintenance-API
client. None of the stored objects ever used it; it is removed.)

**Spec:** `ip`, `role`, `network` (`address`, `gateway`, `dns[]`, optional
`vlan`, `bond` — descriptive, none required), `rack`, `zone`,
`serviceClass`, optional `rdmaInterface`, `hostname`.

`spec.ip` is capped at 45 characters and validated by CEL `isIP()`. That is
*stricter* than the `net.ParseIP` the `v1alpha1` webhook used: it rejects
IPv4-mapped IPv6 (`::ffff:1.2.3.4`) and zoned addresses (`fe80::1%eth0`),
both of which used to be accepted. No stored node is affected.
`spec.rack` and `spec.zone` carry a 63-character cap and a label-value
pattern that also admits the empty string — `rack` was previously unbounded
and is projected onto a Node label, where an over-long value fails at label
write time with no admission error to explain it.
`spec.serviceClass` no longer admits `""`; omit the field instead.
`network.address` is length-bounded and documented as free-form: the name
says address, every stored value is a CIDR, and nothing enforces either — so
the freeze bounds the length and declines to guess a semantic pattern.

**Status:** `conditions[]`, `kubeletVersion`, `capacity`, `allocatable`,
`nodeName`, `observedGeneration`. There is no `status.phase` — see "No
`status.phase`" above; `v1alpha1` still serves one, projected from
`Ready.reason`. `talosVersion`, `lastHeartbeat`, and `providerID` were
removed pre-freeze: none had a writer or a reader anywhere in the controller,
SDK, or UI (`serverClassRef` was already dead and documented as such before
this cleanup, and is removed with the same evidence).

**Printer columns:** `Phase` (the `Ready` condition's `reason`), `Ready`,
`Role`, `ServiceClass`, `Zone`, `Age`.

**Controller:** finalizer-guarded; secondary-watches core `v1.Node` and maps
it back to its FrameNode (`nodeToFrameNode`) to keep the `Ready` condition and
versions in sync. It branches on the `Ready` condition's `reason` directly,
not on the `v1alpha1` projection.

### Node labels Hearthrack writes

The FrameNode controller projects five labels onto the corresponding
`corev1.Node`, and strips them when the FrameNode is deleted. **These are
API.** Two other components select on them, so renaming one unschedules
running workloads at runtime with no admission-time error to warn anyone.

| Key | Source | Read by |
|---|---|---|
| `frame.plume-labs.io/rack` | `spec.rack` | operators; topology-aware placement |
| `topology.kubernetes.io/zone` | `spec.zone` | Kubernetes' own well-known zone key |
| `frame.plume-labs.io/service-class` | `spec.serviceClass` | the inference provider's `NodeSelector`; the FrameJob controller's Workflow labels |
| `frame.plume-labs.io/role` | `spec.role` | operators |
| `frame.plume-labs.io/rdma` | `"true"` when `spec.rdmaInterface` is set | operators |

Empty values are not written: a label that is absent means "unclassified",
and there is no separate "explicitly empty" state.

`rack` lives under `frame.plume-labs.io/`, not `topology.kubernetes.io/`.
The well-known keys in the `kubernetes.io` namespace are `zone` and `region`;
`rack` is not one of them and that prefix is reserved for upstream use. Hearthrack
wrote `topology.kubernetes.io/rack` before `v1beta1`; the controller removes
that key on every reconcile so an existing node relabels itself.

**One key, two meanings.** `frame.plume-labs.io/service-class` on a **Node**
is the tier of hardware the FrameNode controller classified. The same key on
a **Namespace** selects which namespaces a `FrameResourceQuota` projects
into. They are unrelated; the shared key is historical and is frozen as-is
because renaming either breaks the other's readers silently.

---

## FrameJob

A workload submitted to the cluster, realized as an Argo `Workflow`.

**Spec:** `pipeline`, `serviceClass` (default `LOW`), `priority`
(critical/high/medium/low, default `medium`), `gpuCount` (0–1024, default 0),
`parameters` (map, at most 64 keys, each value at most 1024 characters),
`suspended` (bool, default false). `pipeline` stays an open string — it names
an Argo `WorkflowTemplate` Hearthrack does not own — but carries DNS-subdomain
form bounds (253 characters, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.…)*$`) so a
malformed value is refused at admission. `serviceClass` and `priority` now
default in the schema as well as in the defaulting webhook, which means the
default is applied *before* CEL evaluates rather than after.

`spec.namespace` was removed in `v1beta1` (F5). The Argo Workflow is created
in the FrameJob's own namespace. A `v1alpha1` read returns the object's own
namespace, not whatever was set. Removal, rather than a
`SubjectAccessReview` on the caller, is what closed the cross-namespace
reach: the SAR is the correct multi-tenant answer and needs
`AdmissionRequest.UserInfo` plumbed into a raw `admission.Handler`, a
`create subjectaccessreviews` grant and a fail-closed story; removal got
there first and is strictly safer.

`spec.name` was removed pre-freeze: it
was `Required` and pattern-validated, but no controller ever read it
(`metadata.name` is used throughout) and the SDK's submit path never sent
it. There is no rule coupling `gpuCount` to `serviceClass`. One used to be
enforced by the webhook (`framejob_webhook.go`'s `validateFrameJob`) for
`gpuCount > 0` jobs at `serviceClass: LOW`, but it only ever ran for the
three pipelines in `knownPipelines` — it silently didn't apply to
`training` or most other real pipelines — and it was deleted outright in
the v1beta1 freeze (F8) rather than repaired, because it coupled two
orthogonal properties: how much hardware a job wants and how preemptible
it is. `validateFrameJob` today only warns, never rejects: it returns a
warning when `pipeline` is outside `knownPipelines`, on the grounds that
`pipeline` names an Argo `WorkflowTemplate` Hearthrack does not own, so
enumerating other people's templates isn't Hearthrack's business. See
`docs/roadmap.md` and
`docs/superpowers/specs/2026-08-09-frame-api-freeze-inventory.md`.

**Status:** `conditions[]`, `argoWorkflowName`, `startTime`,
`completionTime`, `message`, `observedGeneration`. There is no
`status.phase` — see "No `status.phase`" above.

**Conditions:** `Ready` only. Its `reason` is the job phase — one of
`Submitted`, `Running`, `Suspended`, `Completed`, `Failed` — and its `status`
is `True` only for `Completed`. The `Submitted` condition type this kind used
to write once and never update is gone (F3). An object still carrying only
that legacy condition projects to `Submitted` at `v1alpha1`, and an object
with no conditions at all projects to `Pending`; neither is inferred from
`completionTime`, which the controller sets on failure too.

**Printer columns:** `Phase` (the `Ready` condition's `reason`), `Ready`,
`Pipeline`, `ServiceClass`, `GPUs`, `Age`.

**Controller:**
- On create: builds an Argo `Workflow` in the FrameJob's own namespace, with
  `spec.suspend`, `priorityClassName` (mapped from `priority` →
  `frame-{priority}`), and arguments `gpu-count` + `service-class`; sets
  `Ready` to `Submitted`.
- On update: syncs `spec.suspended` → `Workflow.spec.suspend` via patch;
  derives the `Ready` reason from workflow status (Argo `Succeeded` →
  `Completed`, etc.); surfaces `Suspended` when `spec.suspended=true` and the
  workflow isn't terminal.
- Secondary watch on Argo `Workflow` objects (label `frame.plume-labs.io/job` +
  `frame.plume-labs.io/job-namespace`) — reacts to Workflow changes instead of
  polling every 30 s.
- On delete: removes the Workflow via finalizer.

---

## SchedulingPolicy

Queue / priority configuration for the HPC scheduler.

**Spec:** `scheduler` (volcano/yunikorn/…), optional `queueName`,
`priorityClass`, `preemption`, `priorityValue` (int32, default 0),
`queueWeight` (int32, 1–10000, default 1). `queueName` and `priorityClass`
carry a Kubernetes-object-name pattern (not an enum — they name objects
created outside this CR). `preemption: true` requires `priorityClass` to be
set (CEL, mirroring the existing webhook check).

> The `preemption` CEL rule is `has()`-guarded on both versions. It has to
> be: `preemption` is `bool,omitempty`, the apiserver defaults on a write at
> the *request* version, and **conversion-webhook output is not re-defaulted**
> — so a `v1alpha1` status patch reached the rule with no `preemption` key at
> all and failed every reconcile with `no such key`. Every `XValidation` in
> `api/` was surveyed for the same shape; this was the only unguarded scalar
> dereference.

`gangScheduling` was removed pre-freeze: it was validated (required
`queueName` alongside it) and shown in the UI, but no controller ever
created a Volcano/YuniKorn `PodGroup` or set a `minMember` — setting it had
zero cluster-side effect beyond the validation rule. Gang scheduling is
unimplemented; see `docs/roadmap.md`'s V1 path, which records that it
belongs on `FrameJob` (a property of the job being scheduled, not the
policy) if someone builds it.

**Status:** `conditions[]`, `observedGeneration`.

**Printer columns:** `Scheduler`, `Queue`, `Ready`, `Reason` (hidden by
default, `-o wide`), `Age` — previously none; `kubectl get
schedulingpolicy` showed only `NAME`/`AGE`.

**Controller:**
- Reconciles a cluster-scoped `PriorityClass` named by `spec.priorityClass`
  with `value=spec.priorityValue` and `preemptionPolicy` derived from
  `spec.preemption`. Uses two labels (`frame.plume-labs.io/policy-namespace` +
  `frame.plume-labs.io/policy-name`) to track ownership across scope boundaries.
- When `scheduler=volcano`: reconciles a `scheduling.volcano.sh/v1beta1/Queue`
  with `weight` and `reclaimable`.
- When `scheduler=yunikorn`: reconciles a `yunikorn.apache.org/v1alpha1/Queue`
  with `weight` and `preemption.allowPreemptSelf`.
- Missing scheduler CRD → `Ready=False` with reason `ReconcileError` and a
  `QueueCRDMissing` Event carrying the detail (graceful degrade, no retry
  storm). Missing PriorityClass CRD → hard fail + retry.
- Cleanup on delete via finalizer (removes PriorityClass + queue).

---

## FrameResourceQuota

Per-service-class resource ceiling. The controller projects it as a
`ResourceQuota` named `frame-<serviceclass>` into every namespace labelled
`frame.plume-labs.io/service-class` with the matching value.

**Spec:** `serviceClass`, optional `maxGPUs` (0–1024), `maxCPU` (Quantity),
`maxMemory` (Quantity), `maxJobs`. At least one of the four limits must be
set (CEL, mirroring the existing webhook check).

**Status:** `observedGeneration`, `conditions[]`, `namespaces` (how many
namespaces this quota projects into) and `used` — the sum of `status.used`
across every projected `corev1.ResourceQuota`, keyed exactly as
`buildResourceList` writes them (`limits.cpu`, `limits.memory`,
`requests.nvidia.com/gpu`, `count/framejobs.frame.plume-labs.io`). A key no
namespace reported is absent rather than zero: "not measured" and "measured
as nothing" are different answers.

**Printer columns:** `ServiceClass`, `Ready`, `Reason` (hidden by default,
`-o wide`), `Age` — previously none.

**Quota mapping:** `maxGPUs` → `requests.nvidia.com/gpu`, `maxCPU` →
`limits.cpu`, `maxMemory` → `limits.memory`, `maxJobs` →
`count/framejobs.frame.plume-labs.io`. `maxJobs` counts FrameJob objects, not
the pods their workflows fan out to, and completed FrameJobs keep counting
until deleted.

**Not in scope:** scheduler queue limits. `SchedulingPolicy` already reconciles
Volcano/YuniKorn queues; projecting the same ceiling into a queue would make
two resources authoritative for one number.

---

## NodeTuning

Node-local kernel and hardware settings — KSM, a `tuned` profile, a MIG
profile — declared once and applied by a per-node agent. **Cluster-scoped**,
**`v1beta1` only**, **no webhook**.

**Spec:** `nodeSelector` (a `metav1.LabelSelector`; omitted means every node),
`tunedProfile`, `ksm` (`enabled`, plus optional `pagesToScan`,
`sleepMillisecs`, `mergeAcrossNodes`), `migProfile`. `ksm.enabled` defaults to
**off** — tuning that costs CPU is opt-in.

Two NodeTunings whose selectors both match a node is a configuration error,
not a merge: the agent refuses the node and leaves it alone rather than
picking a winner.

**Status:** `observedGeneration`, `nodes[]`, and a `conditions[]` that nothing
currently writes. Each `nodes[]` entry carries:

| Field | Meaning |
|---|---|
| `phase` | `InSync`, `Drifted`, `RebootPending`, `Applying`, `Failed` |
| `realization` | `FullyRealized`, `Effective`, `Pending` — see below |
| `appliedGeneration` | the spec generation the agent last wrote to disk |
| `observed` | what the node **measures**, never what was written: `ksm.memoryKSM` (from `systemctl show -p MemoryKSM`), `ksm.generalProfit`, `ksm.pagesSharing` (sysfs counters), `tunedProfile` (tuned's active profile) |
| `restartedAt` / `restartedGeneration` | when the unit was verified back up, and for which generation |
| `message` | why a node is `Failed` or held |

`Effective` vs `FullyRealized` is the distinction the whole design exists for:
a drop-in written to disk is **not** a setting in effect until the unit
restarts. `Effective` means the live knobs took; `FullyRealized` means the
restart-gated ones did too.

**Printer columns:** `TunedProfile`, `KSM`, `Age`.

**Controller** (`internal/controller/frame/nodetuning_controller.go` +
`nodetuning_rollout.go`): diffs desired against **observed** and reports. It
never disrupts a node on its own. When a change needs a unit restart, the node
is held at `RebootPending` until it carries an explicit approval:

```bash
kubectl annotate node <node> frame.plume-labs.io/tuning-approved=<generation> --overwrite
```

The value is the `metadata.generation` being approved. Approving generation 4
says nothing about generation 5 — each disruptive change is approved on its
own.

Once approved, the rollout half takes over: cordon → evict through the
eviction API (so PodDisruptionBudgets are honoured) → ask the agent for a
detached restart → wait → uncordon. Four properties are load-bearing, and each
is a scar:

1. **A restart is verified by the unit's `ActiveEnterTimestamp` moving**, never
   by the node flapping `NotReady`. `k3s-agent` returns in seconds while a node
   only reports `NotReady` after ~40 s of missed lease — a wait keyed on
   readiness reports failure on success. Readiness is still required before
   uncordoning; it is the precondition for handing workloads back, not the
   evidence anything restarted.
2. **Every probe tolerates failure.** Restarting k3s on the server node takes
   the apiserver down for seconds, so the controller's own reads are *expected*
   to fail mid-wait.
3. **One node at a time, and never while another node is not Ready.** Losing
   one node is a rolling operation; losing two on a three-node cluster is an
   outage.
4. **A failure halts the campaign.** The node stays cordoned, the reason lands
   in its status entry, and no other node is touched.

**Node agent** (`cmd/agent`, `Dockerfile.agent`, DaemonSet in
`deploy/kubernetes/base/node-tuning-agent/`): runs on every node including
tainted ones and the control-plane server, `priorityClassName:
system-node-critical`, privileged with `hostPID` so its host calls are
`nsenter`ed into PID 1's namespaces, `smartctl` aside. It observes every 30 s, applies what
matches, and publishes node state through annotations
(`frame.plume-labs.io/tuning-*`) that the controller reads.

Because privileged + `hostPID` is host-root-equivalent, **the set of units the
agent may restart is a compile-time allowlist**, not anything reachable from a
spec: `k3s`, `k3s-agent`, `kubelet`, `containerd`. Exact match, never a prefix
— that allowlist and the fixed `smartctl` command are the agent's security boundary.

**Node prerequisite:** `tuned` must already be installed on any node a
NodeTuning sets `tunedProfile` on. It is *not* in the agent image and cannot
be, since `tuned-adm` resolves to the node's own binary. Ubuntu Server does not
ship it. Until it is present the node fails loudly and stays `Drifted`, which
is the correct report for an unconfigured node. KSM and the MIG label need
nothing installed. `deploy/scripts/node-tuning-install.sh` does the install,
and the two other things `kubectl apply` cannot do — see
[deployment.md](deployment.md).

---

## FrameMachine

*Has a controller, **no** webhook and **no** `v1alpha1`* — it postdates the
freeze, the same as NodeTuning and FrameTask, but unlike NodeTuning it is
**namespaced**. Short name `fm`. Design in
[`docs/superpowers/specs/2026-09-10-lot1-hardware-redfish-design.md`](superpowers/specs/2026-09-10-lot1-hardware-redfish-design.md),
including "What a real iLO4 turned out to do" — the section every claim below
traces back to.

A physical server's baseboard management controller (BMC), polled over
Redfish. This is deliberately a new kind rather than fields on `FrameNode`:
`FrameNode` converts from `v1alpha1`, and every field added to it would have
to round-trip through a conversion that cannot represent it, the same
reasoning that gave `FrameTask` its own kind in lot 2.

**Spec:** `bmc.address` (the management port's IP; CEL `isIP(self)`, capped
at 45 characters to bound that rule's cost like `FrameNodeSpec.IP`, and
loopback/link-local are refused — the field is a server-side-request-forgery
primitive, since the controller connects to it carrying credentials, and
requiring a literal IP takes name resolution out of that path), `bmc.credentialsRef`
(a Secret in the same namespace holding `username`/`password`; only the
controller reads it — see [deployment.md](deployment.md), "RBAC", for why no
console tier holds `secrets`), `bmc.tls` (`insecureSkipVerify` or
`caBundleRef` — one is **required**, CEL-enforced, deliberately with no
permissive default: an iLO4 ships a self-signed certificate, so an
unconfigured machine sits `Reachable=False` with `TLSError` until someone
chooses), `powerRequest {action, requestedAt}` (one of `On`,
`GracefulShutdown`, `ForceOff`, `ForceRestart`, `ClearSEL`,
`IndicatorLedOn`, `IndicatorLedOff` — acted on only when `requestedAt` is
strictly later than `status.lastPowerActionAt`, the same restartedAt-style
timestamp pattern lot 2 uses to restart a Deployment, chosen because a
converged "desired: On" would fight someone who pressed the physical power
button and could not express a restart at all), `nodeRef` (optional, names a
Kubernetes node, not a `FrameNode` — a Kubernetes node name is true
regardless of how the machine was installed or classified).

**Status:** `powerState`, `postState` (the BMC's own POST-state string),
`indicatorLED`, `inventory`, `sensors` (**nil whenever the reading cannot be
trusted** — see below), `sensorsValidAt` (deliberately distinct from
`lastProbeAt`: a probe can succeed against a powered-off machine and still
not have taken a trustworthy sensor reading), `eventLog` (most recent 25
entries — etcd is not a log store) plus `eventLogCounts`/`eventLogTotal`,
`lastProbeAt`, `lastPowerAction`/`lastPowerActionAt` (the guard) and
`lastPowerActionError` (cleared on the next success; carries why a power
action failed **without** touching `Reachable`, since a power action can
fail — most often on a privilege gap, see [deployment.md](deployment.md) —
while the BMC answers everything else fine). No `status.phase` — see "No
top-level `status.phase`" above; health is `Reachable`, not `Ready` (see the
note there).

**Why sensors go to nil instead of being kept beside a caveat.** A real
iLO4 replays its last cached Thermal/Power reading as a live one — the
captured ML350 Gen9 reported CPU1 at 40°C, `Status.State: Enabled`, with 46
sensors online whether powered off, mid-POST or finished, twenty minutes
after being unplugged in a 20°C room
(`internal/redfish/testdata/ilo4-real/PROVENANCE.md`). Nothing in the
reading, its reported health, or the sensor count distinguishes a
measurement from a memory, so the controller derives trustworthiness from
`PowerState` and the HPE OEM `PostState` instead
(`Snapshot.SensorsTrustworthy`, `internal/redfish/client.go`) and clears
`status.sensors` outright when it is not met, rather than shipping a frozen
reading next to a flag nobody reads. See [runbook.md](runbook.md) for the
six states the console's `sensorAvailability` distinguishes and why a
*stale* (as opposed to untrustworthy) reading is shown, not hidden.

**Conditions:** `Reachable` only, with reason `Probed` on success and one of
`TLSError`, `AuthFailed`, `Timeout`, `ConnectionRefused`, `Unsupported`,
`CredentialsUnavailable`, `ProbeFailed` otherwise — full vocabulary and remediation in
[runbook.md](runbook.md).

**Printer columns:** `Address`, `Power`, `Reachable`, `Model`, `Node`, `Age`.

**Controller** (`internal/controller/frame/framemachine_controller.go`):
polls `RequeueAfter: 60s` on success, `5m` after a failed probe or a Redfish
client it could not even build; a failed probe deliberately does not clear
previously stored inventory, sensors or the event log — the console shows
the last good reading beside its age rather than wiping the panel on a
transient hiccup. `spec.powerRequest` is executed at most once per
`requestedAt`, before the probe that follows in the same reconcile, so a
power action is never lost even when that probe then fails.

**"GracefulShutdown" is not itself a Redfish `ResetType` on this hardware.**
The captured iLO4's `Actions.#ComputerSystem.Reset` lists `On`, `ForceOff`,
`ForceRestart`, `Nmi` and `PushPowerButton` — no `GracefulShutdown`. The CRD
enum keeps that name because it is what a person means and the enum is a
frozen API; `internal/redfish/client.go`'s `resolveResetType` substitutes
`PushPowerButton` — the ACPI power-button signal an installed operating
system chooses whether to honour — when `GracefulShutdown` itself is not
listed, and returns `ErrUnsupported` when neither is — which surfaces in
`status.lastPowerActionError`, not in `Reachable`: this comes out of
`Reset()`, and a power action's failure never touches the `Reachable`
condition (see above). The `Reachable=Unsupported` reason is a distinct,
probe-time diagnosis — the address answered but did not look like a Redfish
service at all — not this. `ForceOff` is never substituted automatically: it
is a hard cut, not a graceful one, and substituting it silently would turn a
request that asked to be graceful into one that was not, with no way for the
caller to know.

**No `gofish`, and no other Redfish library.** `internal/redfish` is a
hand-written `net/http` + `encoding/json` client behind a `Client`
interface, tested against `httptest.Server`s serving the recorded iLO4 JSON
above. Seven endpoints against one firmware family is not where a
general-purpose vendor-abstraction library pays for itself, and the risk it
would answer — fields this firmware omits — is handled by pointer decoding
directly instead.

**Not in this lot, and deliberately:** remote console and virtual media (both
sit behind iLO4's paid Advanced licence and don't speak Redfish anyway);
in-band IPMI over KCS as a fallback (dead exactly when the lot exists for —
a powered-off machine has no OS to expose `/dev/ipmi0` through — and not
free, since reaching it needs a privileged DaemonSet Pod Security `baseline`
refuses); discovery by scanning a subnet (machines are registered, not
found). Full reasoning for all three in the design document.

**Deployment status: nothing here has been exercised against a live BMC.**
No HPE iLO was reachable when this lot was designed; a real ML350 Gen9 was
reached once, on 2026-09-10, and 26 of its HTTP responses were captured
across three power states to test the client against — but no console has
ever been opened against a live machine, no power action has ever been sent
to real hardware, and the captured machine carries no operating system, so
the `PostState` (and sensor behaviour) a *booted* machine reports has never
been observed. See [deployment.md](deployment.md), "Registering a machine's
BMC" and "The check that has never once been run end to end", for the
prerequisites and the unexecuted verification sequence.

---

## StorageHealthPolicy

`frame.plume-labs.io/v1beta1`, cluster-scoped. The thresholds behind
FrameMachine's storage conditions and alerts. A singleton: the apiserver
refuses any name but `default`. It has no status. The FrameMachine controller reads its thresholds on each pass, and the
`zfsscrub` controller its `spec.scrub`, and the absence of the
object means the defaults below.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `spec.zfsCapacityPercent` | integer, 1–100 | 80 | Raises `StorageCapacityHigh`; clears two points below. |
| `spec.ssdWearPercent` | integer, 1–100 | 80 | Raises `DiskWearHigh`. |
| `spec.scrub.nodeSelector` | label selector | none | Nodes whose ZFS pools Hearthrack scrubs. Nil matches none. |
| `spec.scrub.intervalDays` | integer, 1–365 | 30 | Time between two completed scrubs of a node. |
| `spec.scrub.windowStart` | `HH:MM` | `02:00` | From when a scrub may start, in `timeZone`. |
| `spec.scrub.windowHours` | integer, 1–24 | 4 | How long the start window stays open. |
| `spec.scrub.timeZone` | IANA name | `UTC` | The zone `windowStart` is read in. Not `Local`: that is the controller container's zone. |

`spec.scrub` absent means Hearthrack starts no scrub. The scrub is planned by its
own controller (`zfsscrub`), one node at a time — see
[storage.md](storage.md#planned-zfs-scrubs). Writing this object is what makes
every selected node read its disks for hours: the `editor` role decides it.

RBAC follows the three tiers, like SchedulingPolicy: an editor can move a
threshold, a viewer only reads it. See [storage.md](storage.md#storage-health-alerts).

## NetworkHealthPolicy

`frame.plume-labs.io/v1beta1`, cluster-scoped singleton named `default`, like
StorageHealthPolicy, with the same three RBAC tiers. Its absence means the
defaults below and no named link.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `spec.minLinkSpeedMbps` | integer, at least 1 | 1000 | Raises `LinkSpeedLow` on a physical link that is up below it. |
| `spec.linkSaturatedPercent` | integer, 1–100 | 80 | Raises `LinkSaturated` on a link or bond averaging this over five minutes. |
| `spec.linkErrorsPerMinute` | integer, at least 1 | 1 | Raises `LinkErrorsRising` at this many errors a minute over five minutes. |
| `spec.links[]` | at most 64 | | Named links, which become required. |
| `spec.links[].node`, `.interface` | string | | The machine's `spec.nodeRef`, the kernel name. |
| `spec.links[].speedMbps` | integer | | Replaces the minimum for this link. |
| `spec.links[].members` | integer, 1–16 | | Members a bond must hold. |

See [network.md](network.md).

## FrameUser

A person who can sign in to the Cluster Control UI. Written by admins and by
`authd`; it has **no controller** — nothing reconciles a FrameUser into cluster
state. It is a record that `authd` reads at sign-in, which is why it is the one
CRD with no entry in the controller table.

**Spec:** `email` (becomes the Kubernetes username, capped at 254
characters), `role`
(`admin` | `operator` | `viewer`, decides the group the issued token carries),
`passwordAuth` (`enabled` | `disabled`, **defaults to `disabled`**). It
currently grants nothing: `authd` serves no password sign-in, so every
account is passkey-only whatever this says (see [api.md](api.md),
"Authentication"). On
`v1alpha1` only, `passwordHash` is also a spec field; see status below.

`state` (`enabled` | `disabled`, **defaults to `enabled`**) decides whether
`authd` may issue the account an identity at all. It is a flag rather than an
absence on purpose: revoking every passkey would deactivate an account too,
but destructively, and only re-enrolment in person would undo it. One
function in `internal/authd` (`requireIssuable`) enforces it, and all three
identity-issuing paths — `POST /auth/token`, passkey login, invitation
acceptance — call it. The load-bearing one is `/auth/token`: the
console polls it every five minutes and refreshes whenever the cached token
is within two minutes of expiry (`src/lib/auth.ts`), so disabling an account
cuts a session that is already open within at most one token lifetime,
without touching the cookie.

**Status:** `passwordHash` (argon2id PHC string, read and written only by
`authd`) and `credentials[]` — enrolled WebAuthn authenticators, each with the
base64url credential `id`, the COSE `publicKey`, the `signCount` as of the last
assertion, `addedAt`, and an optional human `label`. Public data only: the
private key never leaves the device. Credentials live in status precisely so an
admin editing an account by hand cannot corrupt a key — and `passwordHash`
joined them there in `v1beta1` (F11), which is the whole asymmetry the security
review raised: the *public* key material was protected from hand-editing while
the password hash sat in a widely-readable spec field. A `v1alpha1` client
still spells it `spec.passwordHash`; the conversion webhook moves it both ways,
so it is the one genuine bijection in the freeze.

**What the move to status protects, precisely.** Writes, not reads — measured
against a real apiserver while writing the RBAC tiers, not assumed. A
principal with `patch frameusers` but no `frameusers/status` cannot alter the
hash: a merge patch carrying both a spec change and a status change applies
the spec half and the apiserver drops the status half silently.

That is the `v1beta1` half. The `v1alpha1` half is the validating webhook, and
it is not optional: RBAC resource names carry no version, so `patch
frameusers` also covers `spec.passwordHash` at the deprecated version, and
conversion carries whatever it finds there into `status.passwordHash` without
re-validation. The webhook therefore rejects **any** main-resource write, at
either version, that changes the hash — including a replace that omits it,
which would otherwise erase the credential and report success. The subresource
is the only way in.

But a plain
`GET frameusers` returns the whole object, status included, so **anyone who
can read a FrameUser at all can read its password hash** — the status
subresource splits writes, not reads. `frameuser-viewer-role` and
`frameuser-editor-role` are still written with no `/status` rule (see
[deployment.md](deployment.md), "RBAC"), but binding either of them is still
handing over the hashes. The destination is a `Secret` — at-rest encryption
and audit treatment a CR field does not have — and that is the only change
that closes the read side. It is recorded here rather than done in the
freeze, because it is a real design change: `authd`'s store gains a second
object to keep consistent, and the last-admin guard would have to survive a
partially-written pair.

**Webhook:** validation refuses to remove the last admin, whether by deletion or
by demotion, and **fails closed** — if the admin list cannot be read, the request
is denied rather than assumed safe. It also refuses any main-resource write
that changes `status.passwordHash` (see above). This is the one webhook in the
tree that declares `matchPolicy: Equivalent` explicitly rather than inheriting
the apiserver default, because the hash guard only reaches a `v1alpha1`
request if the apiserver converts it first.

Validation also refuses a non-admin changing `spec.state`, for the same reason
it refuses a role change, and refuses disabling the last admin. "Last admin"
now means the last **enabled** admin: a disabled admin cannot obtain a token
by any route, so counting one would allow the last usable admin to be
demoted, deleted or switched off behind an account nobody can sign in to.

**This field was added after the freeze**, on 2026-09-09, and it is a field
on an existing kind rather than a new kind — a stronger break than
NodeTuning's or FrameTask's. It ships with the four debts those raised
already paid: the RBAC tiers for `frameusers` already exist, no controller
was added, this reference moved with it, and it arrives with a test that
proves the *effect* (a disabled account is refused an identity on all four
paths) rather than the field's presence. It exists on both served versions
and is carried in both directions of the conversion, so `v1alpha1` is not
lossy for it.

**Deployment status:** `authd` is consumed now — the `frame-uiproxy` sidecar
verifies its tokens and impersonates the `FrameUser` it names (see
[deployment.md](deployment.md), "RBAC"). That wiring is merged but **not yet
rolled out**: the live test cluster still runs the pre-lot operator, and the
`frameusers.frame.plume-labs.io` CRD installed there holds **zero objects**,
so the `spec` → `status` move had nothing to migrate yet. See
[deployment.md](deployment.md)'s "Rollout order" for the sequence that
changes that.

---

## FrameTask

*No controller, no webhook, no `v1alpha1`* — it postdates the freeze, the
same as `NodeTuning`, but unlike `NodeTuning` it is **namespaced**, in the
`uiproxy` sidecar's own `TASK_NAMESPACE` (`frame-system` by default). The
trace of one mutating request the `frame-uiproxy` sidecar made to the
apiserver on a person's behalf: who, what verb, against which object, and
how it ended. See [deployment.md](deployment.md), "RBAC", for how the
identity in `spec.user` gets there, and `internal/uiproxy/recorder.go` for
the code that writes this kind — there is nothing else that does.

**Spec:** `user` (the impersonated Kubernetes username — the `FrameUser`'s
email; required, 1–254 characters), `verb` (`create` | `update` | `patch` |
`delete` — reads are never recorded), `target` (an `ObjectRef`: `group`,
`resource`, `namespace`, `name`, `subresource` — `resource` is the plural path
segment the proxy parsed the request URL into, not a `Kind`, since deriving a
`Kind` from it would need a RESTMapper for nothing the Tasks screen renders),
and `action` (an optional human label from the UI's `X-Frame-Action` header
— "cordon node w2" — absent when the request did not come from the console,
in which case the Tasks screen falls back to `<verb> <target>`).

> `target.subresource` is the trailing segment of the request path when there is one — `exec`, `log`, `scale`, `eviction` — and empty for a request on the object itself. It exists because without it a shell opened in a pod records as `create pods/<name>`, the same string a pod create produces, so the product's decision to record exec sessions would have had no visible effect. **Added post-freeze, on 2026-09-09 (lot 2), and it is a field on an existing kind** — the same class of break as `FrameUser.spec.state`, and cheaper: `FrameTask` is `v1beta1`-only with no conversion webhook, so there is no second version to keep lossless and no conversion function to write.
>
> One consequence worth stating: `verb` for an exec is `create`, not `get`, even though the request arrives as an HTTP GET. A browser opens an exec as a WebSocket upgrade and `new WebSocket()` can issue nothing else; the apiserver authorizes it as `create pods/exec` regardless, and the record uses the word the RBAC rule uses so the two can be read together.

**A `FrameTask` for an exec is a session, not a request.** It opens when the WebSocket is established and closes when the socket closes, so `status.startedAt` and `status.finishedAt` bracket the whole shell and a three-hour session is visible as three hours. `status.httpCode` is **101** for a session that opened — `Succeeded`, not `Failed`, despite being below 200. `spec.action` is built by the recorder rather than supplied by the console (`open a shell in <ns>/<pod> (<container>)`), because a WebSocket carries no `X-Frame-Action` header.

**A dry run leaves no `FrameTask` at all.** The manifest editor validates every edit with a `PUT ?dryRun=All` before it writes, which is also how it learns which fields changed; a dry run stores nothing, so recording it would put two rows in the trail for one edit.

There was a `ref` field here, pointing at an object carrying the action's own
progress. It is gone, and the reason is worth stating so it is not re-added
on the same reasoning: it had no producer and could not have one. A `ref` is
only meaningful when a write on object A produces progress on some other
object B, and every write the console makes is either terminal (cordon,
scale, queue weight) or a create whose progress lives on the object it just
created — where `ref` would only repeat `target`. It shipped as a field
nothing wrote, backing UI that could never render.

**Status:** `phase` (`Running` | `Succeeded` | `Failed`), `httpCode` (the
apiserver's status code, `0` while running), `message`, `startedAt`,
`finishedAt`. There is no `Ready` condition — see "No `status.phase`" above
for why this kind and `NodeTuning` are both exceptions to that rule, and for
different reasons: `NodeTuning` describes several nodes and needs a
dimension conditions do not give it, while `FrameTask` describes one
finished (or in-flight) HTTP call and never has more than one dimension of
health to report in the first place.

**Printer columns:** `User`, `Action`, `Phase`, `Code`, `Age`.

**Written by:** `TaskRecorder` (`internal/uiproxy/recorder.go`), not
impersonation — it authenticates as the pod ServiceAccount so that a viewer,
who cannot create a `FrameTask` under their own impersonated identity, can
still leave the trace of their own refused request. `Start` creates the
object and sets `Running` before the request is forwarded; `Finish` closes
it under a `defer`, so a panic mid-request still closes the record rather
than leaving it `Running` forever, with `httpCode` mapped straight from
whatever the apiserver answered (`2xx` → `Succeeded`, anything else →
`Failed`).

**Retention:** the sidecar purges finished tasks older than seven days on an
hourly sweep (`TaskRecorder.Purge`, `cmd/uiproxy/main.go`). A task still
`Running` is never purged by age — one that never finished is a bug worth
seeing, not a cleanup target.

**RBAC:** two separate grants, easy to conflate. The pod ServiceAccount
itself holds `create`, `get`, `list`, `delete` on `frametasks` and
`update`/`patch` on `frametasks/status` (`cluster-control-impersonator` in
`deploy/kubernetes/base/rbac.yaml`) — distinct from its `impersonate`
grants, and what `TaskRecorder` actually uses, since it always writes as the
SA rather than as an impersonated user. Separately, `frametask-viewer-role` /
`-editor-role` / `-admin-role` exist like every other kind's tier roles and
are aggregated the same way into `frame-viewer`/`frame-editor`/`frame-admin`
— so an impersonated human editor or admin *can* create, patch or delete a
`FrameTask` directly, through `kubectl` or the SDK, under their own RBAC.
Nothing in the console does that today; the console only ever reads them.

---

## FrameService

*Group `services.plume-labs.io`, not `frame.plume-labs.io`.* A declared
instance of a service — inference today; database, queue and VM are future
provider types on the same envelope. Designed in
`docs/superpowers/specs/2026-08-08-frame-service-catalog-design.md`.

**One generic CRD, not one per type.** `spec.type` selects a Go provider
(`internal/services/provider/`) registered at manager startup. The provider,
not the CRD's OpenAPI, owns and validates `spec.parameters`.

> **`spec.parameters` is provider-owned and sits outside the API compatibility
> guarantee.** The envelope below it — `type`, `serviceClass`, `binding`,
> `deletionPolicy`, `status` — is what this project's compatibility promise
> covers. A provider that needs a breaking parameter change ships a new
> `type` value rather than redefining an existing one's parameters.

**Spec:** `type` (required, closed set enforced by the webhook against the
provider registry, plus schema form bounds in `v1beta1`: 1–63 characters,
`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` — which permanently rules out provider
names like `vector_db` or `openWebUI`, and is only ever relaxable, never
re-tightenable), `parameters` (map, provider-owned; at most 64 keys,
each value at most 1024 characters), `serviceClass` (`HIGH`/`MEDIUM`/`LOW`,
default `MEDIUM` — decides scheduling
tier, never a node), `binding.secretName` (defaults to the FrameService's own
name), `binding.projectTo` (namespaces to copy the credentials Secret into,
default none), `deletionPolicy` (`Retain` default | `Delete`).

`serviceClass` carries two meanings, deliberately. It selects the node pool
and the `FrameResourceQuota` the instance's workloads belong to, **and** it
determines the instance's scheduling priority: `HIGH`/`MEDIUM`/`LOW` map onto
the `frame-high`/`frame-medium`/`frame-low` PriorityClasses that
`SchedulingPolicy`'s controller creates. There is no `spec.priority` on a
FrameService and no `spec.priorityClassName`: a long-lived instance's tier is
its urgency, and letting a user name an arbitrary PriorityClass would break
the invariant that Hearthrack owns placement. If a HIGH-tier instance ever needs
to be evicted before a MEDIUM one, that is a `v1beta2` problem (F10).

This mapping only *names* a PriorityClass — it does not create one. Unless a
`SchedulingPolicy` object exists with `spec.priorityClass` set to exactly
`frame-high`, `frame-medium` or `frame-low` (see
`config/samples/frame_v1beta1_schedulingpolicy.yaml`), the named
PriorityClass does not exist on the cluster, and the apiserver rejects every
pod naming a PriorityClass it cannot find — the instance becomes
unschedulable outright, not merely unprioritised. `FrameJob.spec.priority`
maps onto the same names through the same failure mode; provisioning
`SchedulingPolicy` objects for all four tiers is an operational
prerequisite this API does not enforce.

**Status:** `conditions[]`, `binding.secretRef` + `binding.endpoint` (never a credential)
+ `binding.projected[]` (every Secret coordinate the controller has actually
written — the sole record it consults for what it may write to or must
delete, never a label on the Secret itself), `sizing` (`gpu`, `gpuMemory`,
`cpu`, `memory` — what the provider derived, reported because nothing in the
spec states it), `provisioned[]` (objects the provider created, so
`kubectl describe` explains an instance without knowing the provider's
internals), `observedGeneration`. There is no `status.phase` — see "No
`status.phase`" above. `v1alpha1` still serves one, and it is the one
projection computed from `Ready.status` and the deletion timestamp rather
than from `Ready.reason`: this kind's reasons are diagnostic
(`UnknownType`, `SizeRefused`, `ModelCacheMissing`, …) and none of them is a
member of the old phase enum.

**Printer columns:** `Type`, `Ready`, `Reason` (hidden by default, `-o
wide`), `Endpoint`, `Age`. `PHASE` is gone with the field; `Ready` replaced
it (R7).

**Controller:** dispatches to the registered provider for `spec.type`;
generic reconcile drives create → status → delete through the `Provisioner`
interface. Owns the Service and the credentials Secret in the FrameService's
own namespace via owner references, so those are garbage collected with the
FrameService regardless of `deletionPolicy`. A projected copy of the
credentials Secret, in another namespace, cannot carry that owner reference —
owner references do not cross namespaces — so its removal is handled
explicitly by the controller's delete path instead, driven by
`status.binding.projected`. Data objects (a PVC, a delegating operator's CR)
also never carry an owner reference — that split is what lets `Retain` keep
them. A finalizer holds the object open until the controller itself has
removed the projected Secrets and, under `deletionPolicy: Delete`, deleted the
objects listed in `status.provisioned`; `Provisioner` has no teardown hook, so
the provider is not consulted at delete time at all.

**Webhook:** validation only, no defaulting. Refuses an unknown `spec.type`,
validates `spec.parameters` against that provider's schema, and runs the
provider's `Size` so an instance that cannot fit is refused by `kubectl
apply` with the numbers named, rather than admitted and left Pending. Also
refuses changing `spec.type` on an existing object — the old provider is no
longer consulted and the new one does not recognise what was provisioned.

### The `inference` provider (`internal/services/provider/inference/`)

The only backend is llama.cpp, so that Pascal cards (compute capability 6.1,
e.g. the Tesla P4) are served too — vLLM and KubeAI need `sm_7.0`+.
The backend is a provider-internal choice: a future card just means a new
provider behind the same `type`, not an API change.

**Parameter schema** (enforced at admission):

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `model` | string | yes | Name of an [InferenceModel](#inferencemodel). One that does not exist is refused, naming those that do |
| `contextLength` | string | no | Pattern `^[0-9]+$`; defaults to the model's `contextLength`, then 4096. Sized against the GPU — too large is refused here, not left to crash-loop |
| `modelCache` | string | no | Name of a PersistentVolumeClaim, already present in the namespace, holding cached GGUF weights. Defaults to `model-cache-pvc` |

Only a deliberately narrow slice of JSON Schema is enforced, on purpose: on
the root schema, `Type` and `Required`; on each property, `Type`,
`Description`, `Enum` and `Pattern`. A provider that sets anything else
(`MinLength`, `Minimum`, `Format`, nested `Properties`, …) panics at registry
construction rather than being silently accepted and never checked.

**Sizing is derived, never chosen** — there is no `plan` field. `Size` reads
`model` and `contextLength` and computes weights + KV cache for that model.
Llama 3.1 8B Instruct at Q4_K_M is 4696Mi of weights; at `contextLength:
"8192"` the KV cache adds 1024Mi, for 5720Mi total, which fits the
manager's default GPU budget of 7680Mi (an 8 GB-class card; the
`--inference-gpu-memory-mib` flag sets it to your GPUs' usable memory). The same model at `contextLength: "32768"` would need 8792Mi and is
refused at admission.

**A PersistentVolumeClaim for model weights is required.** The provider
mounts the PVC named by `parameters.modelCache` (or `model-cache-pvc` by
default) read-only at `/models` — it does not create or populate it. If that
PVC does not exist, the instance degrades with reason `ModelCacheMissing`
rather than crash-looping; create the PVC (or point `modelCache` at an
existing one) before the FrameService can become Ready.

**Binding.** The provider generates a 32-byte API token and passes it to
llama.cpp as the `LLAMA_API_KEY` environment variable, keeping it out of the
pod spec (`kubectl describe pod` / Events are a much wider audience than
Secret readers). The token is persisted in a Secret the provider owns,
`<service>-inference-key`, separate from the controller's own binding
Secret, and reused across reconciles rather than regenerated. The binding
Secret the controller writes (`spec.binding.secretName` or the FrameService's
name) carries that same token plus the endpoint — an endpoint alone would
protect nothing while looking like a credential. Rotation is out of scope for
this part: the token is written once at provisioning and stays valid until
the instance is recreated.

**What it creates:** a Deployment running llama.cpp with the sized resource
requests (including `nvidia.com/gpu`) and a node selector derived from
`serviceClass`, a Service in front of it, and the two Secrets above.

---

### InferenceModel

Cluster-scoped: `services.plume-labs.io/v1beta1`, plural `inferencemodels`. One
entry of the catalog, with no status: nothing reconciles it, the `inference`
provider reads it on each pass. The name is the file: `/models/<name>.gguf` on the
instance's model cache.

| Field | Required | Meaning |
|---|---|---|
| `weightsMiB` | yes | Weights on the GPU, CPU offload already taken off. |
| `layers`, `kvHeads`, `headDim` | yes | KV cache per token. `layers` counts those holding a growing cache: a hybrid model's linear-attention layers do not. |
| `hostMemoryMiB` | no | The pod's memory. Default twice `weightsMiB`. |
| `contextLength` | no | Default context of an instance that states none. Passed as `-c`. Slots share it. |
| `parallel` | no | `--parallel`. Absent leaves llama.cpp's choice. |
| `kvCacheType` | no | `f16`, `q8_0` or `q4_0`: `--cache-type-k` and `--cache-type-v`. Sizing follows it. A quantised V needs flash attention, which llama.cpp decides: a card that cannot do it fails at startup. |
| `cpuMoELayers` | no | `--n-cpu-moe N`, with every other tensor on the GPU: the experts of the first N layers stay in host memory. Needs `hostMemoryMiB`. Passes `--threads` too. |
| `threads` | no | `--threads`, and the pod's CPU. Absent keeps 4 CPUs. |
| `reasoningBudget` | no | `--reasoning-budget`: tokens, `-1` no limit, `0` none. |
| `reasoningBudgetMessage` | no | `--reasoning-budget-message`. Needs `reasoningBudget`. |

An option left out adds no flag. The image is fixed per Hearthrack release, by digest
(`server-cuda-b11277`, the build served on a Tesla P4): moving it means checking the
flags above on the new build, and restarts every instance. The manager creates the three defaults
(`llama-3.1-8b-instruct`, `llama-3.1-70b-instruct`, `qwen2.5-7b-instruct`) when it
starts on a cluster with no InferenceModel at all, so a default deleted from a
catalog that has anything else in it stays deleted, unless an instance still names
it. A seed that fails is logged and the manager carries on, retrying every 30
seconds. Until it passes, inference instances report `SizeRefused`, or an event
when the catalog cannot be read at all, and their Deployments stay as they are.

An edit reaches instances on their next pass, within ten minutes, and changing a
flag or the pod's resources restarts them. An instance runs one pod under
`Recreate`, so the old pod goes first: a flag llama.cpp refuses takes it down, with
the exit code in its status and the reason in the pod's log, and resources no node
has leave it `Unschedulable`. If an instance no longer
fits, it degrades with `SizeRefused` and its Deployment is left as it was.
Deleting a model an instance serves is refused at admission; with webhooks off,
that instance degrades the same way. A FrameService update that leaves
`spec.parameters` alone is not sized again, so a model edited under it does not
hold up its labels, or its deletion.

---

### The `postgres` provider (`internal/services/provider/postgres/`)

A cluster of the [Zalando postgres-operator](https://github.com/zalando/postgres-operator),
which must be installed: Hearthrack writes the `postgresql` CR and the operator does
the rest. The instance's name has the operator's form, `<team>-<cluster>`.

| Parameter | Required | After creation | Meaning |
|---|---|---|---|
| `instances` | yes | moves | Pods, primary included. Above one, a topology spread keeps them on different nodes. |
| `volumeSize` | yes | grows | Size of each volume, e.g. `20Gi`. A smaller value degrades with `VolumeShrinkRefused`. |
| `nodes` | no | moves | Comma-separated node names, replacing any node affinity. Absent leaves placement alone. |
| `storageClass` | no | fixed | Empty is the cluster default. |
| `postgresVersion` | no | fixed | Default `18`. |
| `database`, `owner` | no | fixed | Default: the team. The binding carries `owner`'s credentials. |
| `walBucketClaim`, `walEndpoint` | no | fixed | WAL-G archive to that ObjectBucketClaim, created if absent. Empty means no archiving. |
| `walRetentionDays` | no | fixed | Default `14`. |
| `create` | no | stays | `true` lets the provider create the cluster. Excludes `adoptUID`. |
| `adoptUID` | no | stays | UID of an existing cluster to take over, compared on every pass. |
| `binding` | no | fixed | `none` publishes no credentials Secret. Set it from the start: changing it later leaves an earlier Secret behind, no longer updated. |

`fixed` means compared, not applied: a parameter that differs from the live
cluster degrades with `SpecDrift` and writes nothing at all. Changing `nodes`,
or going above one instance, changes the pod template, so the operator restarts
the pods.

**A cluster is never recreated.** One is created only with `create: "true"`, by a
FrameService that never served, on a name with no `pgdata-<name>-0` volume left.
Otherwise a missing cluster degrades with `AdoptionTargetMissing`,
`ClusterMissing` or `InstanceLost` and waits for a restore: a new CR under the
same name would come up without its users or settings, or empty.

**A cluster is adopted only by UID, and that UID is checked on every pass.** An
existing cluster this FrameService does not claim degrades with
`ClusterNotOwned`, the message giving its UID. The label alone is not enough: the
claim records the UID it was made on (`services.plume-labs.io/claimed-uid`), so a
CR restored from a backup, which keeps both under a new UID, counts as unclaimed.
Hearthrack leaves it alone until `adoptUID` names it, and that pass compares
`instances`, `volumeSize` and `nodes` too, whichever way the cluster came.

With `adoptUID` matching and no drift, including on `instances`, `volumeSize` and
`nodes`, the provider writes the label `services.plume-labs.io/frameservice` and
the annotations `helm.sh/resource-policy: keep` and `claimed-uid` on the CR, label
and `keep` on the bucket claim, and nothing else: the spec and `metadata.generation` do not move. It leaves a chart's
own labels in place; retiring the chart is a separate step, and the CR survives
it only through that annotation. A bucket claim labelled for another FrameService
is refused.

**`deletionPolicy` never reaches the data.** The CR and the bucket claim carry
no owner reference and are not in `status.provisioned`, and the manager has no
`delete` verb on either, so `Delete` behaves as `Retain` for them. Removing a
database is a deliberate `kubectl delete` with the operator's delete annotations.

The binding Secret carries `host`, `port`, `database`, `username` and
`password`, copied from the operator's credentials Secret: the manager needs
`frame-manager-secret-writer` in the namespace. With `binding: none` there is no
Secret and the manager needs no access to Secrets there, for consumers that read
the operator's Secret themselves. Other Ready reasons: `Provisioning`,
`ClusterNotRunning`, `OperatorMissing`, `InvalidName`.

---

### The `cache` provider (`internal/services/provider/cache/`)

One [Valkey](https://valkey.io) server, run by Hearthrack as a single-replica
StatefulSet with a Service of the same name, placed by `serviceClass`. No
operator: see [the design](superpowers/specs/2026-11-07-frameservice-cache-valkey-design.md).
There is **no replica and no automatic failover**: the server is back when the
kubelet or the scheduler brings it back.

| Parameter | Required | After creation | Meaning |
|---|---|---|---|
| `profile` | yes | fixed | `cache`: `allkeys-lru`, nothing persisted, a restart empties it. `queue`: `noeviction`, AOF with `appendfsync everysec`, for BullMQ. |
| `maxMemory` | yes | moves | Dataset ceiling, e.g. `512Mi`. Moving it restarts the server. The pod's memory is 1.25× this for a cache, 2× for a queue (an AOF rewrite forks), plus 64 Mi. |
| `volumeSize` | queue only | grows | At least three times `maxMemory`: an AOF rewrite holds the old base, its log and the new base at once. A smaller value than the volume degrades with `VolumeShrinkRefused`. Growing needs a StorageClass with `allowVolumeExpansion`; without it the instance degrades with `VolumeGrowRefused` and nothing else moves. |
| `storageClass` | no, queue only | fixed | Empty is the cluster default. |
| `restoredVolumeUID` | no, queue only | stays | Says the volume of that UID is the one wanted, after `InstanceLost`. |

`fixed` means compared: a different value degrades with `SpecDrift` and writes
nothing. Lowering a queue's `maxMemory` under the data it holds gets the server
killed replaying its AOF, until the value goes back up. The name must be a
DNS-1035 label, starting with a letter, of at most 52 characters, or the instance
degrades with `InvalidName`.

**Nothing is taken over.** A StatefulSet, Service or `<name>-valkey-auth` Secret
this FrameService does not control, or a `data-<name>-0` volume without the label
`services.plume-labs.io/frameservice: <name>`, degrades with `NotOwned` before
any write. This provider adopts nothing.

**A queue that lost its volume says so.** One that has served and has no volume
degrades with `InstanceLost` and starts nothing. Hearthrack detects more than it can
prevent: with the StatefulSet still there and its pod gone, the StatefulSet
controller makes a new empty claim under the same name and label on its own, and
the server starts on it. So the volume's UID is recorded on the StatefulSet
(`services.plume-labs.io/volume-uid`), and a different UID degrades with
`InstanceLost` and stays there, the message naming both. Consumers may reach the
server meanwhile, and it takes their jobs. To go on, restore what is needed and set
`restoredVolumeUID` to the volume's UID: a restored claim has a new one, so nothing
else clears it. With no volume at all, restore one under `data-<name>-0` with the
label above. The record goes with the StatefulSet: deleting it forgets the alarm,
and a replaced volume is taken for the original.

**`deletionPolicy` never reaches the volume.** It carries no owner reference and
is not in `status.provisioned`, and the manager has no `delete` verb on volumes,
so `Delete` behaves as `Retain`. A FrameService recreated under the same name
mounts it again, in the `queue` profile; recreated as `cache`, it ignores it.
Removing the data is a deliberate `kubectl delete pvc`.

The password is generated into `<name>-valkey-auth`, and reaches the server in
a config file from that Secret: it is in no argument and no pod spec. The binding Secret
carries `host`, `port`, `password` and `url` (`redis://:<password>@<host>:6379`).
Both need `frame-manager-secret-writer` in the namespace; without it the instance
degrades with `SecretAccessForbidden`. To rotate, delete that Secret or edit its
`password`: Hearthrack rewrites the config and restarts the server, within ten minutes
since it does not watch Secrets. A pod that restarts in between fails its probe
until then. A Secret left with no `password` degrades with `AuthSecretInvalid`.

The pod asks for 100m of CPU and is limited to 2, which is what a quota with
`maxCPU` counts. A pod the apiserver refuses stays at `Provisioning`: the
StatefulSet records it only as a `FailedCreate` event, in `kubectl describe
statefulset`. `replicas` is held at 1; other edits to the StatefulSet stay until
the next change of settings. A claim not bound yet is not grown, and a growing
volume does not hold `Ready` back. Other Ready reasons: `Provisioning`, `Running`.

The image is fixed per Hearthrack release (`valkey/valkey:9.1.3-alpine`): upgrading
Hearthrack across an image change restarts every instance.

## FrameInstall

`frame.plume-labs.io/v1beta1`, namespaced, short name `fi`. One operating-system
installation on a registered machine, driven through its BMC: attach installer
media, install, reboot, join or create a k3s cluster. A **one-shot, destructive**
act with a terminal phase, not a declaration that is reconciled back into place;
installing again means a new object. The runbook is [deployment.md](deployment.md#provisioning-a-machine-with-ubuntu);
[provisioning.md](provisioning.md) records the design decisions.

Every destructive input is named explicitly; no field means "figure it out".

| Field | Type | Meaning |
|---|---|---|
| `spec.machineRef` | string | The `FrameMachine` whose BMC drives the install. |
| `spec.confirmSerial` | string | The machine's serial, **retyped by hand**. The controller refuses unless it equals what Redfish reports. A typo is a refusal, not an install somewhere else. |
| `spec.hostname` | DNS label | Hostname of the installed system. |
| `spec.network.address` / `.gateway` | CIDR / IP | Static configuration the installer writes. |
| `spec.network.dns[]` | 1–3 IPs | At least one resolver, checked by the apiserver: with none, the installed system cannot resolve the archive it fetches updates from. |
| `spec.network.bond.members[]` | 2–8 MACs | Optional LACP bond over ports named by permanent MAC. |
| `spec.network.vlan` | `id` 1–4094, `parentMAC` | Optional tagged VLAN. It rides on the bond when there is one (`parentMAC` empty); otherwise `parentMAC` names its port. The switch port must be hybrid, see [network.md](network.md). |
| `spec.layout.kind` / `.disks[]` | `single-disk` (1 disk) or `mirror` (2) | Disk recipe. Each disk is a `/dev/disk/by-id/` path (never `sdX`: kernel names reorder between boots) plus `sizeBytes`, asserted on the machine before partitioning. There is no raw escape hatch; an unusual layout is added as a named kind. |
| `spec.cluster.mode` | `init` or `join` | `join` needs `serverURL` and `joinTokenRef` (a Secret holding the node token); `k3sVersion` is always required. |
| `spec.bootMode` | `UEFI` (default) or `Legacy` | Set on the machine, not inherited: an image that boots in the other mode stays on a black screen. |
| `spec.sshKeyRef` | string | A Secret with `id` and `id.pub`. Only the public half reaches an installer image. |

**Status:** `phase` is `Pending`, `Preparing`, `MediaAttached`, `Installing`,
`Installed`, `Joining`, `Ready` or `Failed`; `failedPhase` says where it
stopped, `message` why. `hostKey` is pinned on first contact (Hearthrack cannot know
it in advance, and baking it into the image would turn the image into a secret
carrier). `nodeName` is the joined node; `kubeconfigSecret` is set only when the
install created a cluster. Conditions and `observedGeneration` as for the other
reconciled kinds. Printed columns: Machine, Phase, Node, Age.

No webhook: the bounds are schema markers and CEL rules alone.

## FrameDiskClaim

`frame.plume-labs.io/v1beta1`, namespaced, short name `fdc`. Claims **one
physical disk** for one purpose, **destroying whatever is on it**. Modelled on
FrameInstall: a one-shot act with a terminal phase, not a long-lived
declaration. Disk inventory itself lives on `FrameMachine`; see
[storage.md](storage.md).

| Field | Type | Meaning |
|---|---|---|
| `spec.machineRef.name` | string | The `FrameMachine` whose disk this is. |
| `spec.byIDPath` | string | A `/dev/disk/by-id/…` path, never `sdX`: on the machine this was designed against, `sdX` order changed on every boot. |
| `spec.serial` | string | The disk's serial, retyped by hand and matched against what the node's agent reported. A path can be stale; a serial read off the object you mean to destroy cannot. |
| `spec.destination` | `wipe` or `ceph-osd` | What the disk becomes. LVM and manual partitioning are deliberately absent: each is a different destructive operation with its own failure modes. |

**Status:** `phase` is `Pending`, `Claiming`, `Ready` or `Failed`, with
`message`, `phaseSince` and conditions. `claimUID` is written onto the **machine**
before the destructive work begins and read back before it is repeated, so a
manager restart cannot replay a destructive sequence: the record that it already
ran lives on the node, not in the controller's memory.

**On this build a claim that would succeed never reaches a phase.** The
controller is wired with `unconfiguredWiper`, a stand-in whose every method
fails: the destructive backend (the part that runs `sgdisk` or `ceph-volume`) is
a physical, human-supervised act deliberately not shipped with a manager
rollout. Only a refused claim reaches a phase, `Failed`. See
[storage.md](storage.md).

Printed columns: Machine, Serial, Destination, Phase, Age. No webhook; schema
markers only.

## FrameOSDReplacement

`frame.plume-labs.io/v1beta1`, namespaced, short name `fosdr`. Replaces one Ceph
OSD's disk end to end: drain, purge, physical swap, wipe of the new disk,
declaration to Rook, backfill. A one-shot act with terminal phases. The full
procedure, the operator account and the failure modes are in
[osd-replacement.md](osd-replacement.md).

**Two steps cannot be undone, and each waits for a human**: the purge of the old
OSD and the wipe of the new disk. Each is confirmed by retyping a serial read off
the disk meant, never by a click.

| Field | Type | Meaning |
|---|---|---|
| `spec.osdID` | integer, 0–65535 | The OSD to replace. **Fixed at creation**, like `serial`. |
| `spec.serial` | string | Serial of the OSD's current disk, retyped at launch; must match what Ceph reports for that OSD. |
| `spec.purgeApproval.serial` | string | Approval no. 1: the old disk's serial, once the OSD is drained and safe to destroy. |
| `spec.wipeApproval.serial` | string | Approval no. 2: the new disk's serial, read on its label, once Hearthrack shows the disk it found. |
| `spec.cancel` | bool | Stops the replacement; cannot be taken back. Before the purge a drained OSD is marked in again; after it, nothing is undone and the status says where things stand. |
| `spec.resume` | integer | Raised by one to retry the current step after a pause. A destructive call whose outcome is unknown is **never re-sent by a resume**: the step pauses again and says what to check by hand. |
| `spec.drainTimeoutMinutes` | integer, 10–1440, default 120 | Bound on the wait for all PGs `active+clean`, after the OSD is marked out and again after the new OSD is in. Past it the replacement pauses; nothing is undone. |

Approvals are **armed**: on entering an approval step the controller clears the
field and records `status.approvalArmedGeneration`; only an approval written from
a later generation counts, and none can be set at creation. A serial that does not
match is ignored and may be retyped.

**Status:** `phase` is one of `Checking`, `Draining`, `AwaitingPurgeApproval`,
`Purging`, `AwaitingDisk`, `AwaitingWipeApproval`, `Wiping`, `Declaring`,
`AwaitingOSD`, `Backfilling`, `Succeeded`, `Paused`, `Cancelled`, `Failed`
(`pausedPhase` names the step a pause interrupted). It also carries `host`,
`machineRef`, `oldDisk` and `newDisk` (by-id path, serial, WWN, size, model,
SMART), the PG count `pgs` as `active+clean/total`, and `events`, the
replacement's own journal (last 64 kept). A run of `*At` timestamps
(`markedOutAt`, `purgeRequestedAt`, `purgedAt`, `wipeRequestedAt`, `wipedAt`,
`declaredAt`, …) are **write-ahead records**: each is written before the call it
names, so a restart can tell "never sent" from "sent, outcome unknown".

The disk wipe is handed to the node's agent through Node annotations
(`frame.plume-labs.io/disk-wipe-request` and `…-result`). The agent wipes only a
disk that its own `lsblk` of the same tick reports at that by-id path with that
serial, whole and free, and only once per request id.

The `rook-ceph-operator` is scaled to 0 for the purge, so Rook does not bring the purged OSD back ([osd-replacement.md](osd-replacement.md)). Printed columns: OSD, Host, Phase, PGs, Age. No webhook; the immutability and approval rules above are CEL on the schema.

## FrameCephAction

`frame.plume-labs.io/v1beta1`, namespaced, short name `fca`. One everyday,
**reversible** Ceph gesture from the console; none destroys data. A one-shot
object: doing it again means a new one. See [ceph-actions.md](ceph-actions.md).

| Field | Type | Meaning |
|---|---|---|
| `spec.action` | `OSDIn`, `OSDOut`, `OSDRestart`, `OSDScrub`, `OSDDeepScrub` or `SetFlag` | The gesture. |
| `spec.osdID` | integer, 0–65535 | The OSD acted on. Required for every action but `SetFlag`, and forbidden with it. |
| `spec.flag` | `noout`, `norebalance`, `nobackfill`, `norecover`, `noscrub` or `nodeep-scrub` | The cluster flag `SetFlag` sets. One active object holds a given flag at a time. |
| `spec.durationMinutes` | integer, 5–1440 (120 when left out) | How long the flag stays set (`SetFlag` only). |
| `spec.release` | bool | Unsets a `SetFlag`'s flag before its time. `SetFlag` only; cannot be taken back. |

The gesture is **fixed at creation**: only `release` may change afterwards (CEL
on the schema), and `durationMinutes` is refused on any action but `SetFlag`.
Deleting the object unsets its flag (a finalizer), see
[ceph-actions.md](ceph-actions.md).

**Status:** `phase` is `Pending` (not sent yet), `Active` (a `SetFlag` whose flag
is set), `Done`, `Refused` (a check refused it; nothing was sent) or `Failed`
(sent and failed, or its answer was lost; **never sent again**). `sentAt` is
written *before* the gesture is sent: once it is there, this object never sends
it again. `expiresAt` is when the flag is unset; `flagWasSet` records that the
flag was already set when the action started, in which case it is left set at the
end because it was not this object's. `unsetSentAt` dates the first attempt to
unset, which is retried until Ceph accepts it. Printed columns: Action, OSD,
Flag, Phase, Expires, Age.

No webhook; schema and CEL only.

## FrameStorage

`frame.plume-labs.io/v1beta1`, **cluster-scoped**, short name `fstor`. One storage
entry in the sense Proxmox VE gives the word: a typed, named place where volumes
of declared content types can live, available on some set of nodes. Background
and the console screen: [storage.md](storage.md).

| Field | Type | Meaning |
|---|---|---|
| `spec.type` | `ceph-rbd`, `ceph-bucket` or `local-path` | What backs the entry. NFS and iSCSI are out: neither exists in the park. |
| `spec.content[]` | 1–5 of `workload`, `model`, `backup`, `artifact`, `scratch` | What may be stored here. A list, because a Ceph pool legitimately holds several kinds. |
| `spec.storageClassName` | string | The StorageClass the entry owns or adopts. |
| `spec.adoptExisting` | bool | **Must be set explicitly** for a class Frame did not create. Without it admission refuses: an entry that silently took ownership of a class with production volumes would delete them on its own deletion. An adopted entry owns nothing and deletes nothing. |
| `spec.nodes[]` | at most 64 | Empty means every node. |

**Status:** `phase` is `Ready`, `Degraded` or `Unknown`. `shared` is **derived from
the type, never declared** (`ceph-*` shared, `local-path` not); it lives in status
because a user who could write it could declare a local disk shared and have
Hearthrack believe it. `capacity` reports `usable` first, with `used` and an
optional `raw` that never stands alone: the park's capacity incident came from
reading raw numbers on a pool whose replication divides them by three. `claims`
counts PVCs using the class and how many carry the `frame.plume-labs.io/usage`
label the content-type webhook selects on; a large total with zero labelled is the
normal state on the day the webhook lands. That webhook (a PVC content-type
admission webhook, `internal/webhook/core/v1`) is described in
[storage.md](storage.md). `adopted` records that the class was
not created here and so is never deleted.

`status.cephPlacement` holds what Prometheus does not export about the Ceph
cluster behind a `ceph-*` entry: each pool's size, `minSize` and CRUSH rule, each
OSD's CRUSH weight (a string, `%.4f`, since CRDs carry no float safely; `0` is a
real weight), read from the ceph-mgr dashboard API with a dedicated read-only
account ([storage-mgr-account.md](storage-mgr-account.md)). Its `state` is
`Collected`, `NotConfigured` (no account Secret yet: the normal state until one is
created), `Error` (the last read failed in whole or part; a list that could not be
read is the last good one, dated by its own `*ReadAt`, never zeroed, which would
read as weight 0) or `AuthRefused` (no login is attempted again until the account
in the Secret changes, because Ceph's failed-login counter only resets on a
successful login). `certificateNotAfter` is the expiry of the pinned certificate.
Nil on a `local-path` entry.

**Webhook:** validating. It refuses an entry that names an existing StorageClass
without `adoptExisting`, and re-runs that check on update only when
`storageClassName` changes, since a Frame-owned entry's class legitimately exists
once the controller has created it.

## FrameAlert

`frame.plume-labs.io/v1beta1`, namespaced. One cluster alert as Hearthrack
received it from Alertmanager, with the relay's delivery record. Named
`fa-<fingerprint>`: Alertmanager's fingerprint is a stable hash of the labels, so
one object follows an alert from firing to resolution without an index. Two
roles, each with its own fields: the **receiver** writes `spec` and
`status.state`/`lastReceivedAt`, the **relay** writes `status.deliveries`, each
with a merge patch holding only its own fields. The receiver records whatever it
is handed: Alertmanager's webhook, or a controller with something of its own to
say (the FrameMachine controller raises storage alerts this way, see
[storage.md](storage.md)), in which case `spec.fingerprint` is not Alertmanager's.
Code: `internal/alerting/`.

| Field | Type | Meaning |
|---|---|---|
| `spec.fingerprint` | hex, 1–64 | Alertmanager's fingerprint. |
| `spec.alertName`, `.severity`, `.namespace` | strings | As labelled on the alert. |
| `spec.labels`, `.annotations` | maps, at most 64 entries | Bounded by the receiver (64 entries, 256-byte keys, 4096-byte values) before they reach the apiserver. |
| `spec.startsAt`, `.endsAt` | time | `endsAt` is empty while the alert fires. |
| `spec.generatorURL` | string, ≤2048 | Link back to the rule. |

**Status:** `state` is `Firing` or `Resolved`. `lastReceivedAt` is refreshed at
most every 15 minutes while the state is unchanged (a change of state is written
at once), because Alertmanager resends unchanged groups every few
minutes and the test cluster's kine cannot absorb a write per resend.
`deliveries[]` (keyed by `subscription`) records, per subscription: the last state
delivered successfully, `attempts`, `lastError`, `lastAttemptAt`,
`lastDeliveredAt`, and `permanentFailure` for a non-retryable answer (401, 400, a
redirect), lifted when the alert changes state or the subscription's generation
changes. `excluded: true` means the entry was recorded without sending anything
because the subscription's filter did not match when the alert resolved: a filter
that widens afterwards must not replay the incident to a tenant who never had it
open. Printed columns: Alert, Severity, State, Age.

## FrameAlertSubscription

`frame.plume-labs.io/v1beta1`, namespaced. A tenant endpoint that receives cluster
alerts: the relay POSTs matching `FrameAlert`s to `spec.url` with a bearer token.

| Field | Type | Meaning |
|---|---|---|
| `spec.url` | `http(s)://…`, ≤2048 | Where alerts are sent. |
| `spec.tokenSecretRef` | `name`, `key` | A Secret in the subscription's own namespace holding the bearer token. **The Secret must carry the label `frame.plume-labs.io/alert-token: "true"`**, otherwise the relay treats it as a permanent misconfiguration and never sends. This stops a subscription, writable by a frame-admin who has no Secret access, from pointing at an arbitrary Secret and exfiltrating its value to the URL. |
| `spec.filter.excludeAlertNames` | list | Defaults to `Watchdog` and `InfoInhibitor`, the two meta alerts that never resolve. |
| `spec.filter.severities`, `.namespaces` | lists | Empty means no restriction. |
| `spec.paused` | bool | Stops deliveries. |

**Status:** `observedGeneration`, `lastSuccessAt`, `lastError`, and
`pendingDeliveries` (always written, even when 0, because the rollout proof reads
`pendingDeliveries: 0`). `computedAt` bounds recomputation to once a minute.
Printed columns: URL, Pending, LastSuccess.

## Webhooks

FrameNode and FrameJob have **defaulting + validation**; the other five —
SchedulingPolicy, FrameResourceQuota, FrameUser, FrameService and
FrameStorage — have **validation** only. **NodeTuning,
FrameTask, FrameMachine, FrameInstall, FrameDiskClaim, FrameOSDReplacement,
FrameCephAction, FrameAlert and FrameAlertSubscription have neither**: their bounds are CRD schema
markers alone, CEL included. For NodeTuning that means nothing rejects a
selector that overlaps another's — the agent detects that at apply time and
refuses the node instead. For FrameTask it means the schema's
`MinLength`/`Required`/`Enum` markers (e.g. `spec.user` must be non-empty)
are the only admission-time check there ever is — consistent with having no
controller to defer anything to either. FrameMachine's bounds (the CEL rules
on `bmc.address` and `bmc.tls`) are likewise schema-only, and a controller
that clears untrustworthy sensors rather than a webhook is what stands
between a stale reading and the screen. Validators enforce required fields
and value ranges (or, for FrameService, dispatch to the provider's own
parameter schema) before a CR is admitted.
Tests:
`internal/webhook/frame/v1beta1/*_test.go` and
`internal/webhook/services/v1beta1/*_test.go`.

The webhooks register on `v1beta1` only. The apiserver's default
`matchPolicy: Equivalent` converts a request arriving at `v1alpha1` into the
storage version before dispatch, so one registration covers both versions.
