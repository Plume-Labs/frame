# CRD Reference

Eleven CRDs across two API groups: ten in `frame.plume-labs.io` and
`FrameService` in `services.plume-labs.io` — a separate group so the service
catalog can move without blocking the `frame.plume-labs.io` freeze (see
[roadmap.md](roadmap.md)). Generated CRDs live in `config/crd/bases/`; sample
CRs in `config/samples/`. Each kind has a controller
(`internal/controller/<group>/`) except FrameUser and FrameTask, and a
webhook (`internal/webhook/<group>/v1beta1/`) except NodeTuning, FrameTask
and FrameMachine.

Eight of the eleven are namespaced and serve both **`v1beta1`** (storage) and
a deprecated `v1alpha1`. **NodeTuning, FrameTask and FrameMachine are the
exceptions**: all three postdate the freeze, so all three have **no
`v1alpha1`** (nothing to convert from) and **no webhook**. NodeTuning is
additionally **cluster-scoped**; FrameTask and FrameMachine are namespaced,
like the eight frozen kinds. NodeTuning and FrameTask also report status as
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

Every kind except `FrameTask` carries a top-level `status.observedGeneration`:
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

No Frame kind has a top-level one. Health is reported through
`status.conditions`, and every kind with a controller writes a `Ready`
condition — with two exceptions, recorded below, and they are exceptions for
two different reasons.

This is a rule, not a drift. A single enum forces the API to pick one
dimension of health out of several and cannot express "provisioned but
degraded", which is why the Kubernetes API conventions have called `phase`
strongly discouraged for new APIs since 2019. **Do not add a `status.phase`
field to a Frame kind.** If a lifecycle needs more than `Ready`, add a second
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
| FrameNode | `Discovered`, `Provisioning`, `Online`, `Degraded`, `Offline`. `status` is `True` only on `Online`. |
| FrameResourceQuota | `Reconciled` on success. A reconcile that fails returns an error and requeues rather than writing a failure reason. |
| SchedulingPolicy | `Applied` on success, `ReconcileError` otherwise (a missing scheduler Queue CRD degrades here rather than hard-failing, and also emits a `QueueCRDMissing` Event). |
| TalosMachineConfig | `Applied`; failures `PatchResolveFailed`, `ClientBuildFailed`, `ApplyFailed`. |
| TalosUpgrade | `UpgradeRequested`, `AlreadyAtVersion`; failures `ClientBuildFailed`, `UpgradeFailed`. |
| FrameService | diagnostic, not a lifecycle: `Reconciled`, `UnknownType`, `NotProvisionable`, `SizeRefused`, `ModelCacheMissing`, and whatever else the provider returns. Read `status`, not `reason`. |
| FrameUser | none — it has no controller. |
| FrameTask | not applicable — it has no `Ready` condition at all; read `status.phase` (`Running`, `Succeeded`, `Failed`) instead, see below. |

> **FrameMachine writes a condition, but not this one.** Its controller
> writes `Reachable`, not `Ready` — a deliberate choice (`conditionReachable`
> in `internal/controller/frame/framemachine_controller.go`): a chassis can
> answer its BMC while powered off, which is not a state any other
> controller in this table would call `Ready`. `Reachable.reason` is
> `Probed` on success; `TLSError`, `AuthFailed`, `Timeout`, `Unsupported`,
> `CredentialsUnavailable` or `ProbeFailed` otherwise — see FrameMachine's
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

A bare-metal machine Frame manages. Bridges a physical/Talos node to its
Kubernetes `v1.Node`.

**Spec:** `ip`, `role`, `network` (`address`, `gateway`, `dns[]`, optional
`vlan`, `bond`), `disk`, `rack`, `zone`, `serviceClass`, optional
`rdmaInterface`, `hostname`. `network.address`, `network.gateway`, and at
least one `network.dns` entry become required once `disk` is set (CEL),
mirroring what the webhook already enforced.

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
the freeze bounds the length and declines to guess a semantic pattern. It is
the one node field written verbatim into a Talos machine config.

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

### Node labels Frame writes

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
`rack` is not one of them and that prefix is reserved for upstream use. Frame
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
an Argo `WorkflowTemplate` Frame does not own — but carries DNS-subdomain
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
`pipeline` names an Argo `WorkflowTemplate` Frame does not own, so
enumerating other people's templates isn't Frame's business. See
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
system-node-critical`, privileged with `hostPID` so every host call is
`nsenter`ed into PID 1's namespaces. It observes every 30 s, applies what
matches, and publishes node state through annotations
(`frame.plume-labs.io/tuning-*`) that the controller reads.

Because privileged + `hostPID` is host-root-equivalent, **the set of units the
agent may restart is a compile-time allowlist**, not anything reachable from a
spec: `k3s`, `k3s-agent`, `kubelet`, `containerd`. Exact match, never a prefix
— that allowlist is the agent's entire security boundary.

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
regardless of how the machine was provisioned, where `FrameNode`'s flow is
Talos-shaped and the estate has moved to a Debian-based image).

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
`TLSError`, `AuthFailed`, `Timeout`, `Unsupported`, `CredentialsUnavailable`,
`ProbeFailed` otherwise — full vocabulary and remediation in
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

## TalosMachineConfig

Declarative Talos MachineConfig application to a node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, and one of
`configPatch` (inline YAML) or `configPatchRef` (ConfigMap key selector) —
exactly one must be set (CEL, mirroring the existing webhook check).
`talosEndpoint` carries a `host:port` pattern (bracketed IPv6 accepted,
e.g. `[fd00::1]:50000`, matching what `net.SplitHostPort` in the webhook
accepts), pushed down from the webhook. `nodeName` carries a DNS-1123
subdomain pattern — net-new validation; no webhook ever checked
`nodeName`'s shape. `talosSecretRef` is a local
`TalosSecretReference { name }` type — one **required** field (F7), and no
`namespace` (F6). The Secret is always read from this CR's own namespace,
which is what an empty `namespace` always meant; the cross-namespace reach
is gone rather than narrowed by RBAC. A `v1alpha1` client may still set
`talosSecretRef.namespace`; it is ignored, and a read returns it empty.

The type stays local rather than becoming `corev1.LocalObjectReference`,
because `LocalObjectReference.Name` is `+optional` and a kubebuilder marker
cannot be attached to a subfield of an external `k8s.io/api` type — the same
limitation that created the local type in the first place. Making `name`
required and reusing the external type were mutually exclusive.

**Status:** `conditions[]`, `observedGeneration` (Ready=True reason
`Applied`; False reasons: `PatchResolveFailed`, `ClientBuildFailed`,
`ApplyFailed`).

**Printer columns:** `NodeName`, `Ready`, `Reason` (hidden by default,
`-o wide`), `Age` — previously none.

**Controller:** reads TLS credentials from the referenced Secret (keys
`ca`/`ca.crt`, `crt`/`tls.crt`, `key`/`tls.key`), resolves the patch,
calls `ApplyConfiguration` gRPC with `mode=AUTO`, updates condition. Retries
with 30 s backoff on transient failures. Uses a finalizer for clean deletion.

---

## TalosUpgrade

A Talos OS upgrade for a single node.

**Spec:** `nodeName`, `talosEndpoint`, `talosSecretRef`, `image`. `nodeName`
and `talosEndpoint` carry the same patterns as `TalosMachineConfig`;
`talosSecretRef` is the same local `TalosSecretReference { name }` type, with
the same required `name` and the same absent `namespace`; `image`
must include a tag (CEL, mirroring the existing webhook check) and is
capped at 255 characters (`MaxLength`, a new limit added solely to keep
that CEL rule's cost bounded, not a mirror of anything the webhook checks).
`preserveData` was removed pre-freeze: it defaulted to `true` but had no
reader — the controller's `c.Upgrade(ctx, image, stage, force)` call is the
deprecated Talos client method, whose signature has no wipe/preserve
parameter at all to pass it to, so the field could not have been honored
without first migrating that call to `LifecycleClient` (out of scope here;
see the deferred-migration note already in
`internal/controller/frame/talosupgrade_controller.go`).

**Status:** `conditions[]`, `observedGeneration` (Ready=True reasons:
`UpgradeRequested`, `AlreadyAtVersion`; False reasons: `ClientBuildFailed`,
`UpgradeFailed`).

**Printer columns:** `NodeName`, `Image`, `Ready`, `Reason` (hidden by
default, `-o wide`), `Age` — previously none.

**Controller:** generation-based idempotency guard — only calls `Upgrade` gRPC
when the `Ready` condition's `observedGeneration` differs from `.metadata.generation`,
preventing re-trigger while the node is rebooting. Uses `stage=false, force=false`.
Retries with 30 s backoff on transient failures.

---

## FrameUser

A person who can sign in to the Cluster Control UI. Written by admins and by
`authd`; it has **no controller** — nothing reconciles a FrameUser into cluster
state. It is a record that `authd` reads at sign-in, which is why it is the one
CRD with no entry in the controller table.

**Spec:** `email` (becomes the Kubernetes username, capped at 254
characters), `role`
(`admin` | `operator` | `viewer`, decides the group the issued token carries),
`passwordAuth` (`enabled` | `disabled`, **defaults to `disabled`** — an account
is passkey-only unless someone deliberately opens the other door). On
`v1alpha1` only, `passwordHash` is also a spec field; see status below.

`state` (`enabled` | `disabled`, **defaults to `enabled`**) decides whether
`authd` may issue the account an identity at all. It is a flag rather than an
absence on purpose: revoking every passkey would deactivate an account too,
but destructively, and only re-enrolment in person would undo it. One
function in `internal/authd` (`requireIssuable`) enforces it, and all four
identity-issuing paths — `POST /auth/token`, password login, passkey login,
invitation acceptance — call it. The load-bearing one is `/auth/token`: the
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
the invariant that Frame owns placement. If a HIGH-tier instance ever needs
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

The only backend is llama.cpp — the cluster's one GPU is a Tesla P4 (Pascal,
compute capability 6.1), which rules out vLLM and KubeAI (need `sm_7.0`+).
The backend is a provider-internal choice: a future card just means a new
provider behind the same `type`, not an API change.

**Parameter schema** (enforced at admission):

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `model` | string | yes | Enum, closed to the provider's model catalog (currently `llama-3.1-8b-instruct`, `llama-3.1-70b-instruct`, `qwen2.5-7b-instruct`) |
| `contextLength` | string | no | Pattern `^[0-9]+$`; defaults to 4096. Sized against the GPU — too large is refused here, not left to crash-loop |
| `modelCache` | string | no | Name of a PersistentVolumeClaim, already present in the namespace, holding cached GGUF weights. Defaults to `model-cache-pvc` |

Only a deliberately narrow slice of JSON Schema is enforced, on purpose: on
the root schema, `Type` and `Required`; on each property, `Type`,
`Description`, `Enum` and `Pattern`. A provider that sets anything else
(`MinLength`, `Minimum`, `Format`, nested `Properties`, …) panics at registry
construction rather than being silently accepted and never checked.

**Sizing is derived, never chosen** — there is no `plan` field. `Size` reads
`model` and `contextLength` and computes weights + KV cache for that model.
Llama 3.1 8B Instruct at Q4_K_M is 4696Mi of weights; at `contextLength:
"8192"` the KV cache adds 1024Mi, for 5720Mi total, which fits the P4's
7680Mi. The same model at `contextLength: "32768"` would need 8792Mi and is
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

## Webhooks

FrameNode and FrameJob have **defaulting + validation**; the other six —
SchedulingPolicy, FrameResourceQuota, TalosMachineConfig, TalosUpgrade,
FrameUser, and FrameService — have **validation** only. **NodeTuning,
FrameTask and FrameMachine have neither**: their bounds are CRD schema
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
