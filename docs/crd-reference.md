# CRD Reference

Eight CRDs across two API groups, all namespaced, all at **`v1beta1`** with
`v1alpha1` still served and deprecated: seven in `frame.plume-labs.io` (this
page's first seven sections) and `FrameService` in `services.plume-labs.io` —
a separate group so the service catalog can move without blocking the
`frame.plume-labs.io` freeze (see [roadmap.md](roadmap.md)). Generated CRDs
live in `config/crd/bases/`; sample CRs in `config/samples/`. Each kind has a
controller (`internal/controller/<group>/`) except FrameUser, and a webhook
(`internal/webhook/<group>/v1beta1/`).

> This page documents **`v1beta1`**, the storage version and the conversion
> hub. What the freeze does and does not promise, the nine differences from
> `v1alpha1`, and the deprecation policy are in
> [upgrading.md](upgrading.md), "API versions and the migration path". A
> `v1alpha1` client is still served and still correct; it gets a deprecation
> warning naming what changed for that kind.

---

### `status.observedGeneration`

Every kind carries a top-level `status.observedGeneration`: the
`metadata.generation` its status was computed from. Compare it to
`metadata.generation` to tell whether the controller has seen the current
spec. Conditions carry their own per-condition `observedGeneration` as well;
the top-level field is the one a client can read without knowing which
condition types this kind writes. `FrameUser` has the field and no writer —
it has no controller.

### No `status.phase`

No Frame kind has one. Health is reported through `status.conditions`, and
every kind with a controller writes a `Ready` condition.

This is a rule, not a drift. A single enum forces the API to pick one
dimension of health out of several and cannot express "provisioned but
degraded", which is why the Kubernetes API conventions have called `phase`
strongly discouraged for new APIs since 2019. **Do not add a `phase` field to
a Frame kind.** If a lifecycle needs more than `Ready`, add a second
condition type and document its reason vocabulary here.

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
the spec half and the apiserver drops the status half silently. But a plain
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
is denied rather than assumed safe.

**Deployment status:** `authd` runs in the `cluster-control` namespace but is
consumed by nothing. The `frameusers.frame.plume-labs.io` CRD is installed on
the test cluster and holds **zero objects**, so the `spec` → `status` move
had nothing to migrate. See the roadmap for the authd stages that switch this
on.

---

## FrameService

*Group `services.plume-labs.io`, not `frame.plume-labs.io`.* A declared
instance of a service — inference today; database, queue and VM are future
provider types on the same envelope. Designed in
[`docs/superpowers/specs/2026-08-08-frame-service-catalog-design.md`](superpowers/specs/2026-08-08-frame-service-catalog-design.md).

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
FrameUser, and FrameService — have **validation** only. Validators enforce
required fields and value ranges (or, for FrameService, dispatch to the
provider's own parameter schema) before a CR is admitted. Tests:
`internal/webhook/frame/v1beta1/*_test.go` and
`internal/webhook/services/v1beta1/*_test.go`.

The webhooks register on `v1beta1` only. The apiserver's default
`matchPolicy: Equivalent` converts a request arriving at `v1alpha1` into the
storage version before dispatch, so one registration covers both versions.
