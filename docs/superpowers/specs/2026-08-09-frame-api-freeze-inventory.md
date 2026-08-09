# Frame API freeze — decision inventory

**Date:** 2026-08-09 · **Phase:** B · **Target version:** `v1beta1`, not `v1`.

Scope: the seven `frame.plume-labs.io/v1alpha1` kinds and `services.plume-labs.io/v1alpha1`'s
`FrameService`, their controllers, their webhooks, and their consumers (`src/lib/frame-sdk.ts`,
the views under `src/`, `config/samples/`, `deploy/`, `test/e2e/`).

This is an inventory, not an implementation. No Go, manifest, or CRD was changed producing it.

**The framing decision is already made:** the target is `v1beta1`. Frame is in beta and needs
capability before it needs a promise it cannot keep. Nothing below proposes `v1`, and nothing below
should make `v1` harder to reach later — but "does this survive to `v1` unchanged" is explicitly not
the bar being applied.

---

## 1. How to read this

### The reversibility tiers

The question that sorts everything is not "how hard is this to code" but **"if we ship `v1beta1`
without doing it, can we still do it afterwards?"**

| Tier | Meaning | After the freeze |
|---|---|---|
| **1 — Irreversible** | Renames, removals, semantic redefinitions, the version topology itself. | Only via a *third* API version and another conversion, or not at all. |
| **2 — One-way (tighten now or never)** | Validation that is looser than it should be. | Can only ever be *loosened* further. Tightening after the freeze rejects objects and clients that were valid the day before. |
| **3 — Reversible / additive** | New optional fields, new status fields, new printer columns, new enum *values*, RBAC, dead manifests, client-side bugs. | Ships in any `v1beta1.z` with no conversion. **These are not freeze decisions.** They are listed so the freeze can stop treating them as blockers. |

A field that can be added later is not a freeze decision. A field whose semantics change is.

### Two mechanisms that constrain every recommendation below

**(a) CRD validation ratcheting is per-schema-node.** When an object is updated, the apiserver skips
a validation rule if *the value at the schema node that rule is attached to* is unchanged. So:

- A rule on `spec.serviceClass` is ratcheted when you edit `spec.suspended` — the `serviceClass`
  node did not change, so the rule does not re-run. **Field-level tightening is safe for stored
  objects.**
- A rule on `spec` (an object-level `XValidation`) re-runs whenever *any part of* `spec` changes.
  Flipping `spec.suspended` changes the `spec` node, so the rule evaluates against the whole spec
  including fields nobody touched. **An over-strict object-level rule permanently freezes an
  existing object's spec: it can never be edited again, in any way.**

The last cleanup pass had to back exactly one of these out (the GPU/`LOW` CEL rule — Fix Round 1,
I2 in `.superpowers/pre-freeze-cleanup-report.md`). Every recommendation below that would add an
object-level rule is flagged **⚠ OBJECT-LEVEL CEL**.

The schema already carries four inherited object-level rules, which the freeze makes permanent:

| Rule | Location |
|---|---|
| network fields required once `disk` is set | `api/frame/v1alpha1/framenode_types.go:60` |
| at least one of `maxGPUs`/`maxCPU`/`maxMemory`/`maxJobs` | `api/frame/v1alpha1/frameresourcequota_types.go:29` |
| `preemption: true` requires `priorityClass` | `api/frame/v1alpha1/schedulingpolicy_types.go:28` |
| exactly one of `configPatch`/`configPatchRef` | `api/frame/v1alpha1/talosmachineconfig_types.go:59` |

I verified against the live cluster that none of them strands a stored object: a server dry-run
`kubectl patch` changing `maxJobs` on `quota-high` and `queueWeight` on `neura-default` — both of
which force the spec-level rule to re-run — succeeded. Nothing needs backing out. But the freeze
should record that these four are now load-bearing forever.

**(b) `status.storedVersions` only grows.** A version cannot be dropped from a CRD while it appears
in `.status.storedVersions`, and it stays there until every stored object has been rewritten at the
new storage version. Choosing the storage version is therefore a decision with a migration attached,
not a manifest edit.

### Counts

| Tier | Count |
|---|---|
| **1 — Irreversible** | **14** |
| **2 — One-way (tighten now or never)** | **8** |
| **3 — Reversible / additive** | **9** |
| **Total** | **31** |

---

## 2. Ground truth: what is actually stored

Read from the live k3s test cluster (`KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml`).
Nothing on it was modified; every probe below used `--dry-run=server`, and I re-read the objects
afterwards to confirm.

All eight CRDs are installed, each with **exactly one version** (`v1alpha1`),
`status.storedVersions: [v1alpha1]`, and `spec.conversion.strategy: None`.

| Kind | Stored objects | Notes |
|---|---|---|
| FrameJob | 2 | both `gpuCount: 0`; one `serviceClass: LOW`; condition type is `Submitted` on both |
| FrameNode | 3 | all have a non-empty `serviceClass`; none uses `serviceClass: ""` |
| FrameResourceQuota | 3 | `generation: 3`, conditions still at `observedGeneration: 2` — see F2 |
| SchedulingPolicy | 1 | `preemption: true` + `priorityClass: neura-high` |
| FrameUser | 0 | |
| TalosMachineConfig | 0 | |
| TalosUpgrade | 0 | |
| FrameService | 0 | |

**This is the single most useful input to the freeze.** Half the kinds have never had an object.
`TalosSecretReference`, `FrameService.spec.binding`, and every FrameUser field can be changed at
essentially zero cost — no conversion has anything to convert. Conversely the two FrameJobs and
three FrameNodes are the only objects a conversion will ever be tested against in practice, and
neither exercises a GPU request, a cross-namespace secret ref, or an empty `serviceClass`.

### Two live probes worth recording

**The GPU/`serviceClass: LOW` constraint does not exist.** Confirmed independently, on the deployed
webhook:

```
$ kubectl apply --dry-run=server -f -   # pipeline: training, serviceClass: LOW, gpuCount: 2
Warning: pipeline "training" not in known list [speculative-decoding neura-training-dag neura-inference-dag]
framejob.frame.plume-labs.io/zz-freeze-probe-lowgpu created (server dry run)

$ ... same object with pipeline: neura-training-dag
Error from server (Forbidden): admission webhook "vframejob-v1alpha1.kb.io" denied the request:
jobs requesting GPUs must use serviceClass HIGH or MEDIUM, got LOW
```

**`FrameNode.spec.rack` accepts a value that is not a legal Kubernetes label value**, and the
controller writes it as one:

```
$ kubectl apply --dry-run=server -f -   # rack: "rack/one with spaces and a very ... long value >63 chars"
framenode.frame.plume-labs.io/zz-probe-badrack created (server dry run)
```

See T1.

---

## 3. Tier 1 — Irreversible

### F1 — Version topology and storage version

**Today.** Every CRD is single-version. `config/crd/kustomization.yaml:15-18` has an empty `patches:`
block holding only the `+kubebuilder:scaffold:crdkustomizewebhookpatch` marker — no
`config/crd/patches/` directory exists. `PROJECT` records no `conversion` webhook for any of the
eight resources. `docs/upgrading.md:153-169` states plainly that no compatibility guarantee exists.

**Options.**

1. `v1beta1` served + storage; `v1alpha1` served, `deprecated: true`, not storage. Conversion webhook
   with `v1beta1` as the hub.
2. `v1beta1` served + storage; drop `v1alpha1` entirely (no conversion webhook). Every stored object
   becomes unreadable; the three FrameNodes and two FrameJobs on the test cluster would have to be
   recreated by hand.
3. Add `v1beta1` as served-only, keep `v1alpha1` as storage. Postpones the migration and keeps the
   old schema authoritative — the worst of both.

**Recommendation: option 1.** `v1beta1` is the hub (`+kubebuilder:storageversion` on the `v1beta1`
types, `conversion.Hub` implemented there, `conversion.Convertible` on `v1alpha1`). Set
`deprecated: true` with a `deprecationWarning` on `v1alpha1` from day one — that warning is the
migration policy's only enforcement mechanism, and it costs nothing.

Option 2 is genuinely defensible *for the four kinds with zero stored objects anywhere*
(`FrameUser`, `TalosMachineConfig`, `TalosUpgrade`, `FrameService`) and I would not argue hard
against it. But a per-kind split makes the CRD manifests inconsistent and the conversion test matrix
irregular for no gain worth the confusion. Convert all eight.

**Reversible?** No. `storedVersions` accumulates the moment the first object is written at the new
version, and the choice of hub decides which Go type every controller and webhook is written
against.

---

### F2 — The `status.phase` / conditions split

**Today.** Three of the eight kinds carry a `status.phase`; five are conditions-only. Only one kind
carries a top-level `status.observedGeneration`.

| Kind | `status.phase` | Condition type written | Top-level `observedGeneration` |
|---|---|---|---|
| FrameJob | yes — `framejob_types.go:85-86` | `"Submitted"` (`framejob_controller.go:110`) | **no** |
| FrameNode | yes — `framenode_types.go:107-108` | `"Ready"` (`framenode_controller.go:243, 289`) | **no** |
| FrameService | yes — `frameservice_types.go:129-130` | `"Ready"` (`frameservice_controller.go:265`) | yes — `frameservice_types.go:144`, written at `frameservice_controller.go:255` |
| FrameResourceQuota | no | `"Ready"` (`frameresourcequota_controller.go:102`) | **no** |
| SchedulingPolicy | no | `"Ready"` (`schedulingpolicy_controller.go:95, 103`) | **no** |
| TalosMachineConfig | no | `"Ready"` (`talosmachineconfig_controller.go:136`) | **no** |
| TalosUpgrade | no | `"Ready"` (`talosupgrade_controller.go:142`) | **no** |
| FrameUser | no | none — **no controller at all** (`PROJECT:87-97` has no `controller: true`) | **no** |

`FrameJob.Status.Phase` is not even marked `+optional` (`framejob_types.go:85-86`) — it has an
`Enum` and an `omitempty` tag but no optional marker, which is a generator inconsistency rather than
a behavioural one.

**Options.**

1. **Conditions-only everywhere.** Remove `status.phase` from FrameJob, FrameNode, FrameService.
2. **Phase + conditions everywhere.** Add `status.phase` to the five kinds that lack it.
3. **Keep the split, document the rule.** Phase exists where an object has a genuine lifecycle
   (job, node, service instance); a policy/quota object is binary and needs only `Ready`.

**Recommendation: option 3, with the rule written down — and this is the one I would most like
overruled if the owner disagrees.**

The Kubernetes convention has a direction: SIG-Architecture has been steering away from `phase`
since 2019 (the API conventions document says new APIs should use conditions and that phase is
"strongly discouraged"), because a single enum forces the API to pick one dimension of health out of
several and cannot express "provisioned but degraded". So "make them all match" points at option 1,
not option 2. Option 2 is the wrong direction even though it is the cheaper change.

I am recommending option 3 anyway, for one reason: **`status.phase` is a printer column on all three
kinds that have it** (`framejob_types.go:114`, `framenode_types.go:152`, `frameservice_types.go:150`)
**and the SDK branches on its exact string values** in `mapJobPhase` (`frame-sdk.ts:706-713`) and
`mapNodePhase` (`frame-sdk.ts:731-740`). Removing it deletes the `PHASE` column from
`kubectl get fj`, breaks both mappers, and buys a convention alignment that nothing else in the
project depends on. The honest position is: the split is defensible, the direction of travel is
toward conditions, and Frame should not add `phase` to anything new — which makes it a *rule*, not a
drift.

**⚠** If option 1 is chosen instead, it must land in the same conversion-compatible change as F1, and
the `v1alpha1 → v1beta1` conversion must be able to *drop* `phase`, which means the reverse
conversion has to reconstruct it from the conditions. That is doable for FrameNode (`Ready` reason
carries the phase — `framenode_controller.go:245`) and not doable for FrameJob (the `Submitted`
condition is written once and never updated; see F3). That asymmetry is itself an argument for
option 3.

**Reversible?** No, in either direction. Removing `phase` is a removal; adding it later gives the
field a meaning it did not have, and conversion has to invent values for stored objects.

---

### F3 — `FrameJob`'s `"Submitted"` condition type

**Today.** `framejob_controller.go:105-115` sets `status.phase = "Submitted"` **and** a condition
with `Type: "Submitted"`, `Status: True`, at the moment the ArgoWorkflow is created — and never
touches the conditions again. The later phase transitions (`framejob_controller.go:124-135`) update
`status.phase` only. Both live FrameJobs show this exactly: `phase: Completed`, condition
`Submitted=True reason=WorkflowCreated`.

Every other condition-bearing kind uses the shared `conditionTypeReady = "Ready"` constant
(`internal/controller/frame/helpers.go:25`).

So a generic "is this object healthy" client — the shape used at `frame-sdk.ts:911`, which reads
`conditions.find(c => c.type === 'Ready')?.status === 'True'` for FrameNode — finds no `Ready`
condition on a FrameJob at all, and a `Submitted=True` condition on a job that failed an hour ago.

**Options.**

1. Rename `Submitted` → `Ready` and make it track the real state (True on Completed, False
   otherwise, with `reason` carrying the phase). Two changes: the type string *and* the semantics.
2. Rename to `Ready` but keep the write-once behaviour. Cheaper, and worse: a permanently-True
   `Ready` on a failed job is more misleading than an honestly-named `Submitted`.
3. Keep `Submitted`, and additionally write a `Ready` condition. Both exist; `Submitted` becomes a
   milestone marker, which is a legitimate pattern.

**Recommendation: option 1.** A condition set once and never updated is not a condition. Fixing the
name without fixing the update is option 2, which is strictly worse than doing nothing. If the
controller work is not affordable in this phase, do **option 3** — adding `Ready` is additive and
Tier 3, and `Submitted` can then be deprecated at leisure. Do not do option 2.

**Reversible?** No. Client code branches on the literal string. Note this is *not* a schema change at
all — `metav1.Condition.Type` is a free string in every CRD — so the conversion webhook has nothing
to do here, and that is precisely the trap: it will not be caught by any conversion test.

---

### F4 — `ServiceClass`: four kinds, four different contracts

**Today.** The same three-valued concept is declared four times, no two alike:

| Kind | Marker | Default | Empty allowed? |
|---|---|---|---|
| `FrameJob.Spec.ServiceClass` | `Enum=HIGH;MEDIUM;LOW`, `+optional` — `framejob_types.go:43-46` | **`LOW`, by mutating webhook** (`framejob_webhook.go:55-57`) — not a CRD default | no (webhook fills it) |
| `FrameNode.Spec.ServiceClass` | `Enum=HIGH;MEDIUM;LOW;""`, `+optional` — `framenode_types.go:98-101` | none | **yes** |
| `FrameResourceQuota.Spec.ServiceClass` | `Enum=HIGH;MEDIUM;LOW`, **`Required`** — `frameresourcequota_types.go:31-34` | n/a | no |
| `FrameService.Spec.ServiceClass` | `Enum=HIGH;MEDIUM;LOW`, `+optional` — `frameservice_types.go:44-47` | **`MEDIUM`, CRD default** | no |

The clients disagree too, in a third way. `JobClient.submit` sends
`serviceClass: spec.serviceClass ?? 'MEDIUM'` (`frame-sdk.ts:2313`) — so a job created through the UI
with no tier chosen becomes **MEDIUM**, while the identical job created with `kubectl` becomes
**LOW**, because that is what the mutating webhook fills in. Two different answers to "unspecified"
depending on which client you used. The read-side fallbacks disagree as well: `crToJob` → `'MEDIUM'`
(`:721`), `crToNode` → `'LOW'` (`:754`), `crToQuota` → `'MEDIUM'` (`:781`).

The empty-string member is live — a dry-run create of a FrameNode with `serviceClass: ""` is
accepted today — but **no stored FrameNode uses it**; all three carry HIGH/MEDIUM/LOW.

**Options.**

1. One shared named Go type `ServiceClass` with `Enum=HIGH;MEDIUM;LOW`, used by all four. Drop `""`
   from FrameNode's enum (absence, not empty string, means "unclassified"). Move FrameJob's default
   from the mutating webhook to a CRD `+kubebuilder:default`. Pick one default value and use it
   everywhere it is defaulted.
2. Keep the asymmetry, document each divergence in the type's own doc comment (what §6 of the
   cleanup report proposed as the fallback).
3. Unify the enum only; leave defaults alone.

**Recommendation: option 1, with two carve-outs.**

- **Drop `""` from FrameNode's enum.** This is a *field-level* narrowing, so ratcheting protects any
  stored object that has it — and none does. Safe.
- **Move FrameJob's default into the CRD schema.** A CRD default applies before CEL and before
  webhooks; a mutating-webhook default applies after CRD defaults. Having one kind default through
  the webhook and another through the schema is exactly the kind of ordering subtlety that produced
  the `has()`-guard bug documented in §2 of the cleanup report.
- **Carve-out A: do not unify the *default value*.** `FrameJob` defaults to `LOW` and `FrameService`
  to `MEDIUM`, and both are right for their kind: an unspecified batch job should be preemptible; an
  unspecified long-lived service instance should not be the first thing evicted. Unify the type, not
  the policy. Document why.
- **Carve-out B: `FrameNode` must stay optional and undefaulted.** A node is discovered before it is
  classified; defaulting it would classify hardware nobody has looked at.

**Reversible?** No, on three counts: dropping an enum member, moving a default between admission
stages (changes what an omitted field becomes), and introducing a shared named type (changes the
generated OpenAPI `$ref` structure and therefore every generated client).

---

### F5 — `FrameJob.Spec.Namespace`: the confused deputy

**Today.** `framejob_types.go:53-64`. The field is a DNS-1123 label with `+kubebuilder:default="default"`,
and the doc comment states outright that it need not match the FrameJob's own namespace. The
controller reads it at `framejob_controller.go:87` (`ns := job.Spec.Namespace`) and creates the
ArgoWorkflow there (`:96-100`), using the manager's cluster-wide `workflows.argoproj.io` CRUD
(`config/rbac/role.yaml:66-77`).

The security review (`docs/superpowers/reviews/2026-08-09-security-review.md`, finding I4, lines
265-300) calls this a confused deputy and is right: a principal who can create a FrameJob in *one*
namespace can make the operator create an Argo Workflow in *any* namespace, referencing any
WorkflowTemplate there, which Argo then executes under that namespace's ServiceAccount. Latent
today only because a single ServiceAccount is the only writer; it becomes live the moment RBAC tiers
are bound to humans — which is what this phase is for.

The review names the fix precisely: **`internal/services/binding.go` already solved this shape.**
Its doc comment (`binding.go:76-136`) is worth reading in full. The relevant principle:
`spec.binding.projectTo` also names other namespaces, but the controller (a) only writes at a
coordinate it has itself recorded in `status.binding.projected` via the status subresource, and
(b) refuses to claim a new coordinate where an object already exists (`claimNewCoordinates`).
Authority comes from a record only the controller can write, never from data the requester supplied.

**Options.**

1. **Remove the field.** The Workflow is created in the FrameJob's own namespace. Simplest, closes
   the finding completely, and matches how every other namespaced controller in the repo behaves.
2. **Keep it, add an opt-in allow-list** — a cluster-scoped or operator-config list of namespaces a
   FrameJob may target, default empty (i.e. own namespace only).
3. **Keep it, gate on a `SubjectAccessReview`** in the validating webhook: the requesting user must
   themselves hold `create workflows` in the target namespace. This is the textbook confused-deputy
   fix and the only one that is *correct* under multi-tenancy rather than merely restrictive.
4. Keep it as-is and rely on the RBAC tiers being namespace-scoped RoleBindings.

**Recommendation: option 1 for `v1beta1`, with option 3 as the documented re-introduction path.**

Option 4 is not a fix — the tier ClusterRoles as shipped are cluster-scoped
(`config/rbac/framejob_editor_role.yaml` is a `ClusterRole`, and nothing binds it; see F14), and even
namespace-scoped the escalation is the operator's RBAC, not the caller's.

Option 3 is the right long-term answer, but it needs `AdmissionRequest.UserInfo` plumbed into the
validator (controller-runtime's typed `CustomValidator` does not receive it — the webhook would have
to drop to a raw `admission.Handler`), a `create subjectaccessreviews` grant, and a fail-closed
story for when the SAR call fails. That is real design work and it is not a freeze prerequisite,
because option 1 gets there first and is strictly safer.

Option 1's cost is small and *measurable*: the field is set in exactly three places —
`config/samples/frame_v1alpha1_framejob.yaml:13` (with a comment at `:11-12` explaining the
cross-namespace behaviour), `test/e2e/e2e_test.go:503` (where it is set to `crNamespace` — the FrameJob's *own*
namespace, so the spec proves nothing about the cross-namespace path it enables), and
`deploy/samples/test-cluster/workloads.yaml:129,134`. I swept `internal/`, `api/`, `src/`, `test/`,
`deploy/`, `config/` and `charts/` for this, not just the first three.

Both live FrameJobs have `namespace: default` and live in `default` — the field is a no-op on every
object that exists. The SDK sends it, defaulted to the FrameJob's own namespace
(`frame-sdk.ts:2315`), and reads it with the same fallback (`:723`), so the client already behaves
correctly if the field disappears.

**A note on the general rule.** F5 and F6 are the same question asked twice: *may a namespaced Frame
CR name a namespace other than its own?* The freeze should answer it once, as a rule, and apply it to
both. My proposed rule: **no field in a Frame spec may name a namespace the CR does not live in,
except through a mechanism that records what it wrote and refuses to touch anything it did not
create** — i.e. the `binding.go` model. `spec.binding.projectTo` passes that test. `FrameJob.spec.namespace`
and `TalosSecretRef.namespace` do not.

**Reversible?** No. Removing a defaulted spec field is a removal, and the conversion has to decide
what a stored `namespace: other-ns` becomes (my answer: it converts to the CR's own namespace, and
the deprecation note says so — a behaviour change that must be announced, not silent).

---

### F6 — `TalosSecretReference.Namespace`: the same reach, on Secrets

**Today.** `talosmachineconfig_types.go:44-54`. Empty means "this CR's own namespace" —
`buildTalosClient` falls back explicitly — and the pattern accepts empty alongside a well-formed
label. The doc comment says the cross-namespace reach is "deliberately unconstrained today" and
points here. The manager holds cluster-wide `get secrets`. Used by both `TalosMachineConfig` and
`TalosUpgrade`.

The blast radius is different from F5 and worse in one respect: it reads a Secret. A CR in a
namespace the caller controls can make the operator read Talos client certificates — i.e. **node
root credentials** — out of any namespace, and any failure surfacing the Secret's contents in a
condition message or an Event would exfiltrate them. (The security review found the logging sweep
clean across 108 log sites and every status assignment, so nothing does that today. That is a
property of the current code, not of the API.)

**Options.**

1. Remove `Namespace` from `TalosSecretReference`, leaving a name-only reference resolved in the
   CR's own namespace. The type becomes structurally identical to `corev1.LocalObjectReference`.
2. Keep the field; narrow the manager's Secret RBAC to a specific namespace via `resourceNames` or
   a namespaced Role.
3. Keep it; add a `SubjectAccessReview` gate as in F5 option 3.
4. Leave as-is.

**Recommendation: option 1.** Zero objects exist of either kind, on any cluster — there is literally
nothing to convert, and this will never again be this cheap. The field is set nowhere in
`config/samples/`, `deploy/`, or `test/e2e/`. Note that option 1 also *undoes* the local
`TalosSecretReference` type's whole reason for existing: it was introduced (cleanup report §2) purely
so `Namespace` could carry a validation marker that `corev1.SecretReference` could not. With
`Namespace` gone, the type can be `corev1.LocalObjectReference` and the CEL-cost workaround
disappears with it.

Option 2 is worth doing *as well* regardless of the field — the manager's cluster-wide Secret read
is broad for what it needs — but it is an RBAC change, not an API one, and it does not belong to the
freeze.

**Reversible?** No — removal.

---

### F7 — `TalosSecretReference.Name`: `Required` or optional

**Today.** `+optional`, `json:"name,omitempty"` (`talosmachineconfig_types.go:37-42`). An earlier
cleanup draft made it `Required` by accident; Fix Round 1 (I5) reverted it on the correct grounds
that it mirrors `corev1.SecretReference`, which is optional. The doc comment explicitly defers the
deliberate decision here.

`TalosSecretRef` itself is `Required` on both `TalosMachineConfigSpec:76-77` and
`TalosUpgradeSpec:43-44`. So today you must supply the reference struct but may leave it empty — at
which point `buildTalosClient` looks up a Secret named `""` and fails at reconcile time with a
condition, not at admission.

**Options.**

1. Make `Name` `Required`. A malformed reference is rejected at admission instead of producing a
   permanently-`Ready=False` object.
2. Keep optional, mirroring `corev1.SecretReference`.
3. Keep optional in the schema, add a webhook check.

**Recommendation: option 1, `Required`.** The mirroring argument was the right reason to *revert an
accident*; it is not a reason to keep the looseness deliberately. `corev1.SecretReference.Name` is
optional because it is a general-purpose type used in contexts where a name may legitimately be
absent. Here it is not one of those contexts: there is no code path where an unnamed Talos secret
means anything. The whole struct is already `Required`; requiring its only load-bearing member is
consistent, not a new constraint.

Zero objects of either kind exist anywhere, so there is nothing to strand. If the answer were
"optional", it would need to stay optional forever — which is the reason this is Tier 1 rather than
Tier 2.

**Reversible?** No — making a field required later rejects objects that were valid before, and
required-ness is one of the few things a conversion webhook cannot paper over (it has no value to
invent).

---

### F8 — The GPU / `serviceClass: LOW` constraint: does it exist at all?

**Today.** It effectively does not. `validateFrameJob` (`framejob_webhook.go:77-89`):

```go
if !knownPipelines[job.Spec.Pipeline] {
    ...
    return admission.Warnings{...}, nil    // ← admitted, and returns
}
if job.Spec.GPUCount > 0 && job.Spec.ServiceClass == "LOW" {
    return nil, fmt.Errorf("jobs requesting GPUs must use serviceClass HIGH or MEDIUM, got LOW")
}
```

`knownPipelines` (`:33-37`) holds three entries: `neura-training-dag`, `neura-inference-dag`,
`speculative-decoding`. Anything else returns early with a warning, so the GPU check never runs —
including for `training`, which is what `config/samples/frame_v1alpha1_framejob.yaml:10` and
`test/e2e/e2e_test.go:502` both use. **Verified live** (§2): `training` + `LOW` + 2 GPUs is admitted;
`neura-training-dag` + `LOW` + 2 GPUs is denied.

**Options.**

1. **Drop the constraint.** It has never been enforced for the pipelines this project actually
   ships, nobody has hit it, and the thing it was protecting against — a GPU job running at
   preemptible priority — is a scheduling concern that `SchedulingPolicy` and the `frame-*`
   PriorityClasses are the real mechanism for.
2. **Fix the bypass**: move the GPU check *above* the known-list early return, so it applies to every
   pipeline. The known-list stays a warning.
3. Fix the bypass *and* express the rule in CEL. **⚠ OBJECT-LEVEL CEL** — this spans two spec fields
   and can only be a `spec`-level `XValidation`.
4. Leave as-is.

**Recommendation: option 2.**

Option 4 is untenable: a security review has now called out a constraint that the type's own doc
comment (`framejob_types.go:28-37`) documents as real. Either it is a rule or it is not.

Option 1 is a genuine contender and I want to be honest that I am not certain option 2 beats it. The
rule as written couples two orthogonal things — how much hardware a job wants, and how preemptible it
is — and there are legitimate workloads that want a GPU on a best-effort basis. What tips it to
option 2 is that Frame has exactly one GPU (a Pascal P4; see the roadmap's S1 hardware note), so a
`LOW` GPU job is not "best-effort" here, it is "will be evicted by the next MEDIUM job and never
finish". The rule encodes a real property of this cluster. **This is decision #2 of the three I would
most want the owner to rule on** — see §7.

Option 3 is where the trap is. Moving the check above the early return costs one statement and
strands nothing:

- both live FrameJobs have `gpuCount: 0` → unaffected;
- `config/samples/frame_v1alpha1_framejob.yaml` is `training` + `HIGH` + 2 GPUs → still passes;
- `test/e2e/e2e_test.go:502-506` is `training` + `HIGH` + 2 GPUs → still passes;
- `deploy/samples/test-cluster/workloads.yaml:129,134` are both `gpuCount: 0` → unaffected.

I checked all four writers, including `test/` and `deploy/`, which the earlier pass missed. Adding
CEL on top buys defence-in-depth against a webhook outage that `failurePolicy: Fail` already
prevents, in exchange for a `spec`-level rule that re-evaluates on every update — including the
`spec.suspended` flip that the sample's own comment documents as the normal operation. Fix Round 1
backed exactly this rule out once. **Do not put it back.**

**Reversible?** Fixing the bypass is a *tightening of admission* with no schema footprint, so
strictly it is Tier 2. It is listed in Tier 1 because it changes what the API *means* — today
`serviceClass: LOW` with GPUs is a valid FrameJob, and after the fix it is not — and because the
freeze is the moment to decide whether the constraint is part of the contract at all.

---

### F9 — `FrameJob.Spec.Pipeline`: open string or closed set

Coupled to F8, and the reason F8's option 2 is not purely mechanical.

**Today.** `Pipeline` is `Required` with no enum, no pattern, no length bound
(`framejob_types.go:39-41`). The known-list is warn-only. The cleanup report (§5) records this as
deliberate — soft-validated for extensibility, and the webhook's own comment agrees.

Contrast `FrameService.Spec.Type` (`frameservice_types.go:27-32`): also a free string in the schema
(`MinLength=1`), but the webhook validates it against the *provider registry* and **hard-rejects** an
unknown value, so a typo fails at admission rather than leaving an instance Pending forever. Same
schema shape, opposite enforcement — and `FrameService`'s is right, because the valid set is
determined by compiled-in code, not by objects a user might create.

`Pipeline` is not like that: it names an Argo `WorkflowTemplate` that lives in the cluster and that
Frame does not own. A closed enum would make Frame's API the gatekeeper for a namespace of objects
someone else creates.

**Options.**

1. Keep it open, keep the warning. Add a `MaxLength` and a DNS-1123-subdomain pattern (it names a
   Kubernetes object, so this is form validation, not policy) — that part is Tier 2.
2. Close it to the three known pipelines. `config/samples/frame_v1alpha1_framejob.yaml:10` and
   `test/e2e/e2e_test.go:502` both use `training` and would both break.
3. Replace the string with a `workflowTemplateRef` object reference, validated for existence at
   admission.

**Recommendation: option 1.** Frame should not enumerate other people's WorkflowTemplates. Option 3
is the cleaner model and worth noting for a future version, but it is a rename with a shape change,
which is exactly the kind of thing that should not be invented during a freeze.

**⚠ Consequence for F8:** with `pipeline` staying open, the GPU/`LOW` rule can never be a
"known-pipeline" rule — it applies to all pipelines or none. That is what F8 option 2 does.

**Reversible?** No — closing it later rejects values that were valid; opening it later is fine, so
this is really the "tighten now or never" shape, but its coupling to F8's semantics puts it here.

---

### F10 — `FrameService` needs a `serviceClass` → `PriorityClass` mechanism

**This is a Phase B deliverable, not an option.** The roadmap records it as proven by S1
(`docs/roadmap.md:121`).

**Today.** `FrameJob` has the lever: `jobPriorityClass` (`framejob_controller.go:226-237`) maps
`spec.priority` → `frame-critical` / `frame-high` / `frame-medium` / `frame-low` and sets it as the
Workflow's `priorityClassName` (`:208-209`). `SchedulingPolicy`'s `reconcilePriorityClass` creates
those `PriorityClass` objects with `GlobalDefault: false`.

The inference provider sets no `priorityClassName` anywhere. Its Deployment pod template
(`internal/services/provider/inference/inference.go:530-599`) sets `NodeSelector` (`:541-543`),
security context, volumes, annotations and container resources — and nothing else. So an instance
always runs at the implicit Kubernetes default of 0, with no field an operator could set to change
it.

**Options for the mechanism.**

1. **Derive from `spec.serviceClass`.** `HIGH`/`MEDIUM`/`LOW` → `frame-high`/`frame-medium`/`frame-low`,
   mirroring `jobPriorityClass`. No new field.
2. **Add `spec.priority`** to `FrameServiceSpec`, symmetric with `FrameJob.Spec.Priority`
   (`critical;high;medium;low`), defaulting from `serviceClass`.
3. **Add `spec.priorityClassName`**, a free string naming any PriorityClass.
4. Put it in `spec.parameters`. (Named only to reject: `parameters` is explicitly outside the
   compatibility guarantee — `frameservice_types.go:34-37` — and scheduling priority is not a
   provider-specific concern.)

**Recommendation: option 1.**

`FrameJob` has two levers (`serviceClass` *and* `priority`) precisely because a job's resource tier
and its scheduling urgency are separable — a HIGH-tier nightly batch can be low-priority. A
long-lived service instance has no such separation: its tier *is* its urgency, for its whole
lifetime. Adding `spec.priority` to `FrameService` duplicates `serviceClass` with no case where they
would differ, and then the two can disagree.

Option 3 breaks the invariant that Frame owns placement (`frameservice_types.go:41-43`: "It never
names a node: Frame decides placement") by letting a user name a `PriorityClass` Frame did not
create — including a system one.

Where option 1 is *not* free, and this must be recorded: `spec.serviceClass` today means "which
`FrameResourceQuota` and node pool this instance's workloads belong to". After option 1 it *also*
means "how preemptible this instance is". That is a new meaning on an existing field, which is why
this sits in Tier 1 rather than being a Tier 3 addition. The upside is that it is the same
overloading `FrameJob` already has via the node-label projection, so it is at least a consistent
overloading.

**Implementation note.** The change is one line in the block at `inference.go:533-599`
(`deployment.Spec.Template.Spec.PriorityClassName = ...`), plus a shared mapping function hoisted out
of `framejob_controller.go:226-237` so the two cannot drift. `setContainer`'s careful
partial-update discipline (`inference.go:775-920`) does not apply — `PriorityClassName` is a scalar
on the PodSpec, not a slice or map, so it can be assigned wholesale.

**Reversible?** No, on the semantics. The mechanism itself (setting the field on the Deployment) is
reversible; deciding that `serviceClass` is the input to it is not.

---

### F11 — `FrameUser.spec.passwordHash` is credential material in a spec field

Raised as **M1** in the security review (line 469).

**Today.** `frameuser_types.go:65-68`. An argon2id PHC string, written only by authd, living in
`spec` — i.e. readable by anything with `get frameusers`, which is what
`config/rbac/frameuser_*_role.yaml` would grant if those files existed (they do not; see F14). The
WebAuthn credentials, by contrast, are deliberately in `status`
(`frameuser_types.go:71-76`: "an admin editing an account cannot corrupt a key by hand").

The asymmetry is the finding: the public key material is protected from hand-editing and the
password hash is not.

**Options.**

1. Move `passwordHash` to `status`, matching `credentials`. Only authd's status-subresource RBAC can
   then write it, and the viewer/editor tiers can be denied `frameusers/status` read.
2. Move it out of the CR entirely into a `Secret` referenced by name.
3. Leave it, and rely on RBAC never granting `get frameusers` broadly.

**Recommendation: option 1 for the freeze, option 2 recorded as the destination.**

Option 2 is correct — a credential belongs in a Secret, which has at-rest encryption and audit
treatment a CR field does not — but it is a real design change (authd's store gains a second object
to keep consistent, and the last-admin webhook guard at `frameuser_webhook.go:70-83` would need to
survive a partially-written pair). Option 1 gets the RBAC separation immediately, is a pure field
move within the same object, and is trivially expressible in the conversion webhook.

**Zero FrameUsers exist on the cluster**, so there is nothing to migrate.

**Reversible?** No — moving a field between `spec` and `status` changes which RBAC verb and which
subresource governs it.

---

### F12 — The node-label projection is API surface

**Today.** `framenode_controller.go:221-232` unconditionally writes four labels onto the
corresponding `corev1.Node`:

```go
node.Labels["topology.kubernetes.io/rack"]         = fn.Spec.Rack
node.Labels["topology.kubernetes.io/zone"]         = fn.Spec.Zone
node.Labels["frame.plume-labs.io/service-class"]   = fn.Spec.ServiceClass
node.Labels["frame.plume-labs.io/role"]            = fn.Spec.Role
// + frame.plume-labs.io/rdma = "true" when RDMAInterface is set
```

and `reconcileDelete` (`:271-275`) removes them. These are not an implementation detail — they are
the contract two other components read:

- `inference.go:51` + `:541-543` — the inference provider's `NodeSelector` is
  `frame.plume-labs.io/service-class: <FrameService.spec.serviceClass>`;
- `framejob_controller.go:196` — the same key on the Workflow;
- `frameresourcequota_controller.go:76, 146` — **the same key on `Namespace` objects**, selecting
  which namespaces a quota projects into.

So one label key carries two unrelated meanings depending on the object it is attached to. Renaming
any of these keys silently unschedules every running FrameService and unbinds every quota.

Two specific problems worth deciding now:

- **`topology.kubernetes.io/rack` is an invented key under a Kubernetes-reserved prefix.** The
  well-known keys in that namespace are `zone` and `region`; `rack` is not one, and
  `kubernetes.io/`-prefixed labels are reserved for upstream use. Frame should use
  `frame.plume-labs.io/rack` — it already owns that prefix and uses it for three other keys.
- **The labels are written unconditionally, including empty values.** A FrameNode with no `rack`
  writes `topology.kubernetes.io/rack: ""`. Legal, but it means "unset" and "explicitly empty" are
  indistinguishable to a selector.

**Options.** (1) Document the four keys as frozen API and leave them. (2) Move `rack` to
`frame.plume-labs.io/rack` now, while there are three FrameNodes on one test cluster and no
production install. (3) Move all four to a single documented block and skip empty values.

**Recommendation: option 3.** Rename `rack`, skip empty values, and add a short "Node labels Frame
writes" section to `docs/crd-reference.md`. The rename costs a one-time relabel of three nodes on one
cluster; after a `v1.0.0` chart ships it costs a migration note and a broken selector for anyone who
wrote one.

**Reversible?** No. A label key is read by selectors on live objects; changing it after release
breaks scheduling at runtime with no admission-time error to warn anyone.

---

### F13 — Turning the conversion webhook on: what has to change

Not a judgement call, but it is irreversible in the sense that shipping it wrong loses data silently.

**What exists.** `config/default/kustomization.yaml:219-234` — the conversion CA-injection
replacements are commented out behind the two scaffold markers:

```
# +kubebuilder:scaffold:crdkustomizecainjectionns    (line 226)
# +kubebuilder:scaffold:crdkustomizecainjectionname  (line 234)
```

The validating and mutating equivalents directly above (`:157-217`) are **live**, and cert-manager's
`serving-cert` is already issued and verified in-cluster (roadmap Phase D). So the certificate
machinery the conversion webhook needs is already working; only the wiring to the CRDs is absent.

**The checklist.**

1. `+kubebuilder:storageversion` on the `v1beta1` root types; `conversion.Hub` on `v1beta1`,
   `conversion.Convertible` (`ConvertTo`/`ConvertFrom`) on `v1alpha1`, for all eight kinds.
2. `SetupWebhookWithManager` for conversion on each type, and `webhooks: conversion: true` in
   `PROJECT` for each of the eight resources.
3. **`config/crd/patches/webhook_in_<plural>.yaml` — eight new files.** This directory does not
   exist. Each adds the `spec.conversion` stanza (`strategy: Webhook`, the service reference, and
   `conversionReviewVersions: [v1]`) to one CRD.
4. Register those eight patches under the `+kubebuilder:scaffold:crdkustomizewebhookpatch` marker in
   `config/crd/kustomization.yaml:15-18`, which is currently an empty `patches:` block.
5. Uncomment the two replacement blocks at `config/default/kustomization.yaml:219-234` and list all
   eight CRDs as targets under each scaffold marker, so cert-manager injects the CA into
   `spec.conversion.webhook.clientConfig.caBundle`.
6. Uncomment `configurations: - kustomizeconfig.yaml` at `config/crd/kustomization.yaml:22-23` —
   without it kustomize does not know to rewrite the service name/namespace inside the conversion
   stanza.

**⚠ The Helm trap, which the parity guard cannot see.** Steps 3–5 are *kustomize patches*. The chart
does not run kustomize: `charts/frame/templates/crds.yaml:14` reads `files/crds/*.yaml`, which
`make helm-sync-crds` (`Makefile:273-277`) copies **verbatim from `config/crd/bases/`** — the
un-patched bases. And `hack/helm-parity.sh:179` **skips `CustomResourceDefinition` entirely** from
its content diff, by explicit design (`:152-156`).

So a chart install would ship eight CRDs with `conversion.strategy: None` while the operator serves
two versions — every read at the non-storage version silently returns the stored object
uninterpreted, and `make helm-crds-check`, `hack/helm-parity.sh` and CI would all be green. This is
the single highest-risk item in the whole phase, because the failure is silent and the guard that
exists to catch this class of drift is blind to it by construction.

The fix has to be one of: put the conversion stanza in the `config/crd/bases/` output itself (via
type markers rather than a kustomize patch, so both paths inherit it), or teach `crds.yaml` to inject
it, or make `helm-parity.sh` stop skipping CRDs. **Decide which before writing any of steps 3–5.**

---

### F14 — What a round-trip conversion test must prove that a unit test does not

A unit test of `ConvertTo`/`ConvertFrom` proves the two functions are mutual inverses **on values you
thought to construct**. Five things it cannot prove, each of which has caused a real data-loss
incident somewhere:

1. **That the round trip is lossless on objects nobody wrote a fixture for.** The apimachinery
   answer is a fuzzed round-trip (`apimachinery/pkg/api/apitesting/roundtrip`): generate random
   `v1beta1` objects, convert down and back, require deep equality. This catches the field you added
   to `v1beta1` and forgot to carry through `ConvertFrom` — which a hand-written fixture, written
   from the same mental model as the conversion function, systematically will not.

2. **That the *lossy direction* is annotated, not dropped.** Where `v1beta1` has a field `v1alpha1`
   does not (every one of F10's, F11's, and any new `observedGeneration`), a `v1beta1 → v1alpha1 →
   v1beta1` trip loses it unless it is stashed in the standard
   `kubectl.kubernetes.io/last-applied...`-style annotation escape hatch. Any client that writes a
   *whole object* through the `v1alpha1` endpoint will otherwise silently erase fields it never knew
   about.

   Worth being precise about who that is here, because it is not who you would guess.
   `src/lib/frame-sdk.ts:578` hardcodes `const VERSION = 'v1alpha1'`, so the UI is an old client —
   but it is a *safe* one: its only update path is `patchSpec` (`frame-sdk.ts:2281-2287`), a
   `merge-patch+json` on `spec` alone, which leaves keys it does not name untouched. Its creates
   (`submit`, `frame-sdk.ts:2303-2321`) POST a full object, which produces a new object missing every
   `v1beta1`-only field — a defaulting problem, not a loss problem. **The dangerous client is
   `kubectl apply`**, which does send whole objects, and which
   `config/samples/kustomization.yaml` and the documented `kubectl apply -k config/samples/` smoke
   test both invoke. Test the annotation round trip against `kubectl apply`, not against the SDK.

3. **That the wiring works.** The conversion functions being correct is orthogonal to the apiserver
   being able to *call* them: the CA in `spec.conversion.webhook.clientConfig.caBundle`, the service
   name/namespace, the port, `conversionReviewVersions`, and the manager actually serving
   `/convert`. Every one of those is manifest plumbing that F13 shows is currently absent, and none
   of it is exercised by a Go test. Proving it needs envtest with the CRDs applied *as kustomize
   renders them*, or a Kind e2e spec that writes at one version and reads at the other.

4. **That existing stored objects survive.** The apiserver converts on read from etcd, so an object
   written under `v1alpha1` before the webhook existed goes through `ConvertTo` the first time
   anything lists it. The test must start from objects created at `v1alpha1` — the five that exist on
   the test cluster are the realistic corpus — not from objects the test itself just created at
   `v1beta1`.

5. **That the storage migration completes.** Until every stored object is rewritten,
   `.status.storedVersions` contains both versions and `v1alpha1` cannot be removed. The test must
   assert `storedVersions == ["v1beta1"]` after the migration step, or the deprecation policy written
   in this phase is unenforceable.

**Recommendation.** Three layers, all required: fuzzed round-trip per kind (unit); an envtest spec
per kind that creates at `v1alpha1` and gets at `v1beta1` and back, against kustomize-rendered CRDs;
and one Kind e2e spec that does the same through a real apiserver with cert-manager-injected CA.
The existing e2e suite already runs one spec per CRD, so the third layer is an extension of a pattern
that works rather than new machinery.

---

## 4. Tier 2 — One-way (tighten now or never)

Each of these is validation that is looser than it should be. None strands a stored object, because
all are *field-level* and therefore ratcheted. All become permanent the day `v1beta1` ships.

### T1 — `FrameNode.Spec.Rack` / `.Zone` are unbounded but projected into node labels

`framenode_types.go:90-96` — no `MaxLength`, no `Pattern`. `framenode_controller.go:224-225` writes
both as Kubernetes label values, which are capped at 63 characters and restricted to
alphanumerics plus `-_.`.

**Verified live:** a FrameNode with `rack: "rack/one with spaces and a very ... long value"` is
accepted at admission. The `r.Patch` at `framenode_controller.go:231` would then be rejected by the
apiserver and the reconcile returns `fmt.Errorf("patching node labels: %w", err)` — so the FrameNode
never reaches `Online`, with an error that names label validation rather than the field the user set.
That is a live latent bug, not just a missing bound.

**Recommendation:** `MaxLength=63` + the label-value pattern
`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$` on both. Field-level, ratcheted, and all three stored
FrameNodes (`rack-01`, `local`) pass.

### T2 — `FrameNode.Spec.IP`'s `Format=ip` marker is non-functional

`framenode_types.go:63-64`. `ip` is not a recognised OpenAPI format (the apiserver knows
`ipv4`/`ipv6`/`cidr`); unrecognised formats are silently dropped. Flagged but left untouched by Fix
Round 1, and carried into this phase's list.

**Verified live:** `ip: "not-an-ip"` *is* rejected — but by the Go webhook's `net.ParseIP`, not by the
schema. So the practical exposure is small (`failurePolicy: Fail` means the webhook cannot be
bypassed), and this is defence-in-depth rather than a hole.

**Recommendation:** replace with `+kubebuilder:validation:XValidation:rule="isIP(self)"`, matching
the `network.dns` fix from Fix Round 1 (I1). `isIP()` is confirmed present in the pinned
`k8s.io/apiserver@v0.35.0` CEL environment. Field-level; both bounds and cost are trivial for a
single string. Add a `MaxLength=45` alongside, as I1 had to, to keep the cost estimator happy.

### T3 — `FrameJob.Spec.Parameters` is an unbounded map

`framejob_types.go:72-74` — `map[string]string`, no `MaxProperties`, no key pattern, no value length.
It is passed straight into the Argo Workflow's parameters.

**Recommendation:** `MaxProperties=64`, key `MaxLength=63` with a DNS-1123-label pattern, value
`MaxLength=1024`. Both live FrameJobs have no parameters at all; the sample has two short ones.

### T4 — `FrameService.Spec.Parameters` is an unbounded map

`frameservice_types.go:34-39`. Same shape, with one important difference: the doc comment states that
`parameters` is deliberately **outside** the API compatibility guarantee, validated per-provider at
admission against a registered JSON Schema.

**Recommendation:** bound the *envelope* (`MaxProperties`, key/value lengths) without touching the
per-provider schemas. The compatibility carve-out is about parameter *meaning*, not about the map
being unbounded — an unbounded map is a resource concern regardless of who owns the keys.

### T5 — Numeric fields with no upper bound

- `FrameJob.Spec.GPUCount` — `Minimum=0`, no maximum (`framejob_types.go:66-70`)
- `FrameResourceQuota.Spec.MaxGPUs` — `Minimum=0`, no maximum (`frameresourcequota_types.go:36-39`)
- `SchedulingPolicy.Spec.QueueWeight` — `Minimum=1`, no maximum (`schedulingpolicy_types.go:65-68`)

`SchedulingPolicy.Spec.PriorityValue` *is* bounded (`-2147483648`..`1000000000`), so the absence
elsewhere is inconsistency rather than policy.

The cleanup report (§5) declined these on the correct grounds that inventing a ceiling is a new
policy decision, not a push-down. It still is — but the freeze is where policy decisions are
supposed to be made.

**Recommendation:** `Maximum=1024` on `GPUCount` and `MaxGPUs` (three orders of magnitude above the
one physical GPU this cluster has, so it constrains nothing real while making an accidental
`gpuCount: 100000` a validation error rather than an unschedulable pod), and `Maximum=1000000` on
`QueueWeight`. If the owner prefers no ceiling, that is a defensible answer — but it has to be
answered now, because adding one later rejects objects that were valid.

### T6 — `FrameUser.Spec.Email` has no length bound

`frameuser_types.go:50-52`. The pattern (`^[^@[:space:]]+@[^@[:space:]]+$`) is deliberately loose,
which is right for email. But this value **becomes the Kubernetes username** in the token authd
issues, and an unbounded username in an audit log and an RBAC subject is a poor idea.

**Recommendation:** `MaxLength=254` (the RFC 5321 limit). Zero FrameUsers exist.

### T7 — `FrameJob.Spec.Pipeline` has no length or form bound

See F9. Independently of the open/closed question, it names a Kubernetes object and should be
constrained like one: `MaxLength=253` plus the DNS-1123-subdomain pattern already used for
`nodeName` (`talosmachineconfig_types.go:63-64`). `training` and all three known pipelines pass.

### T8 — `FrameService.Spec.Type` has only `MinLength=1`

`frameservice_types.go:30-32`. The closed set is enforced by the webhook against the provider
registry, which is the right mechanism (unlike F9's case, the valid set genuinely is compiled-in).
Add `MaxLength=63` and a lowercase-alphanumeric pattern so a malformed type fails on form before it
reaches registry lookup. Zero FrameServices exist.

---

## 5. Tier 3 — Reversible / additive (not freeze blockers)

Listed so the phase can stop treating them as gates. Every one of these can ship in a `v1beta1`
patch release with no conversion.

### R1 — Top-level `status.observedGeneration` on the seven kinds that lack it

Only `FrameService` has it (`frameservice_types.go:144`, written at `frameservice_controller.go:255`).
All seven others set `ObservedGeneration` per-condition only.

**What a consumer cannot tell without it.** Concretely, on the live cluster right now: all three
`FrameResourceQuota` objects are at `metadata.generation: 3` with their `Ready` condition at
`observedGeneration: 2`. So the controller has not yet reconciled the current spec — and to learn
that, a client must (a) know a `Ready` condition exists, (b) find it by type, and (c) compare its
`observedGeneration`. A client that reads a kind whose condition types it does not know, or a kind
whose controller writes several conditions with different `observedGeneration` values, has no
answer at all. `status.observedGeneration` answers "has the controller seen this spec yet" without
knowing anything about the kind's condition vocabulary. That is the whole point of it.

It also silently produces wrong UI today: `frame-sdk.ts:911` reads `Ready=True` without checking
`observedGeneration`, so a quota edited a second ago shows as Ready against the *old* spec.

**Recommendation:** add it to all seven. Purely additive — a new optional status field that old
clients ignore. Do it, but do not let it gate the conversion change.

### R2 — `FrameUser` has no RBAC tier roles at all

`config/rbac/` contains admin/editor/viewer triples for seven kinds. There is no
`frameuser_admin_role.yaml`, `frameuser_editor_role.yaml`, or `frameuser_viewer_role.yaml`, and
`charts/frame/templates/_helpers.tpl:59-81`'s `frame.tierRoleCRDs` list omits `frameuser` too. So the
one kind holding credential material (F11) is the one kind with no tier. Additive; generate them.

### R3 — The tier roles ship `*` verbs and are bound to nobody

`config/rbac/framejob_admin_role.yaml:20-21` and the chart template
(`charts/frame/templates/rbac-tier-roles.yaml:21-22`) both grant `verbs: ['*']` on the resource.
Nothing in the repo creates a `ClusterRoleBinding` or `RoleBinding` for any of them — confirmed by
grep across `config/`, `charts/`, and `deploy/`.

Two separate points, and the roadmap conflates them:

- **`services.plume-labs.io` is already covered.** `config/rbac/services_frameservice_{admin,editor,viewer}_role.yaml`
  exist and are registered at `config/rbac/kustomization.yaml:25-27`, and the chart helper includes
  `services-frameservice`. The roadmap's "extend them to the new API groups" is done for the one kind
  that group has.
- **`*` should become an explicit verb list.** `*` on a resource also covers verbs that do not exist
  yet; an admin tier that automatically acquires any future subresource is not a frozen tier.
  Replace with the explicit set plus `deletecollection`, and grant `<resource>/status` write only to
  admin.

Additive/corrective, and it does not touch the schema — but the roadmap's phrase "match the frozen
schema" does have one schema dependency: whatever F11 decides about `passwordHash` determines whether
`frameusers/status` can be granted to the viewer tier at all.

### R4 — `InferenceRoute`: a kind that does not exist, in manifests that ship

`deploy/jobs/pipeline-parallelism.yaml:160-175` and `deploy/jobs/speculative-decoding.yaml:161-174`
both declare:

```yaml
apiVersion: frame.plume-labs.io/v1alpha1
kind: InferenceRoute
```

with a full `spec` (`routes`, `backend`, `serviceClasses`, `weight`, `fallbackBackend`). No such kind
exists — no Go type, no CRD, no controller, no entry in `PROJECT`. Applying either file fails with
`no matches for kind`.

This is the mirror image of the writer-sweep lesson: the last pass missed *writers of fields that had
been removed*; here are two writers of a *kind that was never added*. Both are in `deploy/`, which is
where that pass was not looking.

**Recommendation:** delete the two stanzas, or move them behind a clearly-marked "not implemented"
comment. Do **not** add the kind to satisfy them — a routing CRD is S2 (SDN) territory and adding a
kind to a group being frozen, to make a stale manifest parse, is exactly backwards. Adding a kind
later is additive, which is why this is Tier 3.

### R5 — Client-side phantom reads in the SDK

Three places where the UI reads something the API does not provide:

- **`frame-sdk.ts:683`** declares `FrameNodeCR.spec.gpuCount` and `crToNode` reads it
  (`:760`, `cr.spec.gpuCount ?? 0`). **`FrameNodeSpec` has no `gpuCount` field** — the Nodes screen's
  GPU count is structurally always 0. Either add the field (additive) or stop reading it.
- **`crToPolicy` (`frame-sdk.ts:773-774`)** hardcodes `maxGPUs: 0, maxCPUs: 0`. `SchedulingPolicySpec`
  has no such fields and never did.
- **`crToQuota` (`frame-sdk.ts:785-787`)** hardcodes `usedCPU: '0'`, `usedMemory: '0Gi'`,
  `usedGPUs: 0`, never wired to the projected `ResourceQuota.status.used`. Already flagged in the
  cleanup report §5.

All three are the same decision in three places: **either project the real value into
`status`, or stop the UI implying it exists.** Adding a status field is additive; my recommendation
is to project `used*` for real (the `ResourceQuota` the controller already creates has it) and delete
the other two phantoms.

### R6 — Enum members no controller ever writes

- `FrameJob.Status.Phase` allows `Pending` (`framejob_types.go:85`); `workflowPhase`'s default case
  returns `"Submitted"` (`framejob_controller.go:254-255`), so `Pending` is unreachable.
- `FrameNode.Status.Phase` allows `Discovering` and `Failed` (`framenode_types.go:107`); the
  controller writes only `Discovered`, `Provisioning`, `Online`, `Degraded`, `Offline`
  (`framenode_controller.go:110, 198, 215, 237, 298-310`). Neither `Discovering` nor `Failed` appears
  anywhere in the file.

The cleanup report (§5) correctly read these as *missing controller behaviour*, not dead schema.
Nothing to do at the API level: the enum is already wide enough, so making the controller write those
states later is a controller change with no schema impact. Listed only to close the item.

### R7 — Printer-column gaps

`FrameUser` has no `Ready`/status column — correctly, since it has no controller. `FrameService` has
`Type`/`Phase`/`Endpoint`/`Age` but no `Ready` column, unlike the four kinds the cleanup pass fixed.
Additive.

### R8 — `omitzero` vs `omitempty`

`frameservice_types.go:160, 168, 176` use `json:",omitzero"` on `metadata`/`status`/`items` where all
seven `frame.plume-labs.io` types use `omitempty`. `omitzero` (Go 1.24) actually omits a zero struct
where `omitempty` does not, so this is a real wire difference, not just style. Harmless, but it should
be uniform across a frozen API. Pick one; `omitzero` is the more correct of the two.

### R9 — `src/lib/frame-sdk.ts:578` hardcodes `const VERSION = 'v1alpha1'`

Every Frame API path in the UI is built by `apiBase` (`:615-617`) from that constant, and
`src/lib/k8s-watch.ts` inherits it. Flipping it to `v1beta1` is a one-line change — but until it is
flipped, **every UI read and write goes through the conversion webhook**, which makes the UI the
primary consumer of a code path nothing currently exercises. F14 point 2 explains why the UI is
nonetheless a *safe* old client (merge-patch, not whole-object PUT). Flip it in the same release as
the conversion webhook so that the number of versions in flight never exceeds what the tests cover.

---

## 6. Sequencing

### Change 1 — the conversion-compatible change (everything lands together)

Everything that alters the shape or meaning of a field has to be in the change that introduces
`v1beta1`, because `v1beta1` is the only artefact that can carry it:

- **F1** version topology, storage version, `deprecated: true` on `v1alpha1`
- **F4** unified `ServiceClass` type, `""` dropped, defaults moved into the schema
- **F5** `FrameJob.spec.namespace` removed
- **F6** `TalosSecretReference.Namespace` removed (and the local type collapsed to `LocalObjectReference`)
- **F7** `TalosSecretReference.Name` → `Required`
- **F11** `FrameUser.passwordHash` → `status`
- **F2** whichever `status.phase` answer is chosen (if option 3 — keep the split — this is a doc change only, and drops out of Change 1)
- **All of Tier 2 (T1–T8)** — validation tightening. Not conversion-relevant in itself, but it must be in the *first* `v1beta1` because it can never be tightened afterwards.
- **F13** conversion webhook wiring, *including* the Helm/kustomize divergence decision
- **F14** the three-layer conversion test suite
- **R9** the SDK's `VERSION` constant, flipped in the same release

**Estimate.** Mechanical: F6, F7, T1–T8, R9, and the eight `config/crd/patches/` files — roughly a day,
most of it regenerating and re-verifying. Needs design: F13's Helm/kustomize divergence (half a day
of deciding, then mechanical), F14's fuzzed round-trip harness (this is real work — the apimachinery
fuzzer needs a scheme, per-kind fuzz funcs for the `resource.Quantity` and `metav1.Time` fields, and
the lossy-direction annotation strategy), and F5's removal, which is small in code and needs a
migration note because it is a silent behaviour change for anyone who set it.

### Change 2 — behaviour, no schema (can land before or after Change 1)

- **F3** `Submitted` → `Ready` with real updates (controller only)
- **F8** the GPU/`LOW` bypass fix (one statement moved in `validateFrameJob`)
- **F10** the `serviceClass` → `PriorityClass` mechanism in the inference provider
- **F12** the node-label rename and empty-value skip

These touch no CRD schema, so they are conversion-neutral. **F10 and F12 should land *before* Change 1**
so the semantics F4 freezes are the final ones. F8 can land immediately — it is one statement and I
verified all four writers still pass.

**Estimate.** F8 and F12 are mechanical (an hour each including the sweep). F10 is small code and a
real design decision (§7). F3 is the only one that needs actual controller work: the FrameJob
reconciler currently writes conditions in exactly one branch, and making `Ready` track the lifecycle
means threading it through the phase-transition path at `framejob_controller.go:124-135` too.

### Change 3 — additive (any time, in any order)

All of Tier 3. **R1** (`observedGeneration`) is the highest-value item in the whole document per unit
of effort and the one I would do first, because it is what makes every other status question
answerable. **R4** (the `InferenceRoute` manifests) should be done before anyone else reads `deploy/`
and concludes the kind exists.

**Estimate.** All mechanical. Half a day for the lot, R5's `used*` projection excepted — that is a
small controller addition (read the projected `ResourceQuota.status.used` back into
`FrameResourceQuotaStatus`) rather than a client fix.

### One ordering constraint that is easy to get wrong

**F12 before F4.** F4 removes `""` from `FrameNode`'s `serviceClass` enum. F12 stops writing empty
label values. If F4 lands first, a stored FrameNode with `serviceClass: ""` (none exist today, but a
`v1alpha1` client can still create one until the version is removed) converts to a `v1beta1` object
whose enum forbids the value it holds — and the label projection is what would surface it, as a
patch failure rather than a validation error.

---

## 7. The three that need the repository owner's judgement, not mine

**1. F2 — `status.phase`.** I recommended keeping the split and writing the rule down, against the
direction Kubernetes convention has been moving for six years. My reasoning is entirely about sunk
cost: three printer columns and two SDK mappers depend on the exact strings. That is a weak argument
against a strong convention, and I know it. Someone who intends Frame to be read by people who know
Kubernetes idiom should probably overrule me and take conditions-only — accepting that the FrameJob
conversion then cannot reconstruct `phase` from conditions (F3 explains why) and that the UI's job
list needs rewriting. This is a taste-and-audience decision about what Frame is trying to look like,
and that is not mine to make.

**2. F8 — whether the GPU/`LOW` constraint should exist at all.** I recommended fixing the bypass, but
the honest position is that the rule couples two orthogonal properties (how much hardware a job wants
versus how preemptible it is), and the only thing that makes it defensible is a hardware fact — one
Pascal P4 — that will stop being true the day the GPU is replaced. Deleting the rule is at least as
defensible as fixing it, and the person who knows whether Frame's GPU inventory is about to change is
not me. What is *not* defensible is the third option: leaving a constraint documented in the type
comment, called out in a security review, and enforced for three pipeline names.

**3. F10 — deriving `FrameService`'s priority from `serviceClass`.** The roadmap makes the mechanism
mandatory but leaves the shape open, and my recommendation gives an existing field a second meaning.
That is exactly the kind of overloading a freeze is supposed to prevent, and I am recommending it
anyway on the grounds that a service instance's tier and its urgency are genuinely the same thing.
If that premise is wrong — if there is a real case for a HIGH-tier instance that should be evicted
before a MEDIUM one — then the right answer is a separate `spec.priority`, and it has to be added in
`v1beta1` with the correct semantics, not bolted on later. The premise is a statement about how Frame
will be operated, which the owner knows and I am inferring.

---

## Decisions taken (2026-08-09, repository owner)

The three above were put to the owner as posed. All three are settled; two went
against this document's own recommendation.

**F2 — conditions only, everywhere.** `status.phase` goes. The convention
argument wins over the sunk cost: three printer columns and two SDK mappers are
a day's work, and a `phase` field removed after the freeze needs a third
version. The consequence F3 names stands and must be handled — conversion
cannot reconstruct `phase` from conditions, so `v1alpha1`'s `phase` becomes a
one-way projection *out* of conditions, computed, never stored.

**F8 — delete the constraint.** The GPU/`LOW` rule couples two orthogonal
properties and rests on a hardware fact with an expiry date. It currently
constrains nothing, so deleting it removes no protection anyone has. The type
comment and the webhook branch both go; the security review's finding is closed
by removal rather than by enforcement.

**F10 — derive priority from `serviceClass`.** The premise is accepted: a
service instance's tier is its urgency, so `serviceClass` maps to a
`PriorityClass` exactly as `FrameJob.spec.priority` already does. This is
irreversible and knowingly gives an existing field a second meaning. Recorded
here so that a later reader finds the reasoning rather than rediscovering the
overloading and assuming it was an accident: if a HIGH-tier instance ever needs
to be evicted before a MEDIUM one, that is a `v1beta2` problem, and this
paragraph is the evidence that the case was considered and rejected rather than
missed.
